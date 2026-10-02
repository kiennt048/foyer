// Package ratelimit bounds how often a key may try something, and says how long
// the caller has to wait when it may not.
//
// It is the one rate-limiting mechanism in the control plane (phone sign-in's
// fixed-window counter lives here too, as `Window`; see window.go for why a send
// limit wants a window and an attempt limit a bucket).
//
// Its first job was the sign-in forms. Every attempt at the console's
// `signIn` and the portal's `loginWithAccount` costs one argon2id computation —
// 64 MiB for as long as it runs — so an unthrottled form is both a password-guessing
// endpoint and a way to spend the appliance's memory. `internal/identity` bounds
// how many hashes run at once; this bounds who gets to ask for one.
//
// ── what the field does, and where this lands ───────────────────────────────
//
//   - **Keycloak**'s brute-force detection is per *user* only, with a temporary
//     lockout that grows by a wait increment. Its well-known gap is the mirror of
//     that choice: nothing is per IP, so one address can walk a username list, and
//     the per-user lockout is itself a way to lock a victim out.
//   - **authentik**'s reputation policy scores the client IP *and* the username
//     independently — each failure costs both a point — and denies when either
//     falls below a threshold.
//   - **Django-axes** keys on the combination by default, which a distributed
//     guess walks straight past.
//
// So: two independent buckets, per IP and per account, as authentik does, and
// the request must fit in both. **A token bucket rather than a lockout**, because a
// lockout is binary and a bucket refills: an attacker holding a victim's account
// "locked" has to keep spending requests to do it, and the victim's next attempt
// after the refill interval works. A success refunds nothing automatically; the
// caller may `Forget` the account key on a successful sign-in, which is Keycloak's
// reset-on-success.
//
// ── what it is not ───────────────────────────────────────────────────────────
//
// **In-process.** Two control-plane replicas get twice the budget, the same stated
// limit as `portal.TokenBucket` for the same reason; v1 runs one replica. And the
// IP is the socket's peer address, never `X-Forwarded-For`: nothing in `deploy/`
// puts a proxy in front of either listener, and a header a client writes is a
// bucket key a client chooses.
package ratelimit

import (
	"container/list"
	"net"
	"net/netip"
	"sync"
	"time"
)

// Policy is one bucket's shape: `Burst` attempts back to back, then one more
// every `Every`.
type Policy struct {
	Burst int
	Every time.Duration
}

// Limiter is a set of token buckets under one policy.
//
// Safe for concurrent use. The map is bounded: keys that have refilled completely
// are swept, because a full bucket is indistinguishable from an absent one, and
// beyond `MaxKeys` a limiter built by `New` refuses a new key outright rather than
// admitting it — a limiter whose memory grows with the number of distinct keys an
// attacker names is itself the unbounded state it exists to bound. One built by
// `NewEvicting` admits the newcomer and forgets the least recently seen key
// instead; see there for when that is the right trade and when it is not.
type Limiter struct {
	policy  Policy
	maxKeys int
	evict   bool

	mu        sync.Mutex
	buckets   map[string]*bucket
	order     *list.List // of string keys, least recently seen at the front
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	seen   time.Time
	elem   *list.Element
}

// DefaultMaxKeys bounds the map. At a few dozen bytes an entry it is a few MiB
// at worst, against the 64 MiB a single hash costs — **provided the keys are a
// few dozen bytes**. The limiter stores what it is given, so a caller keying on
// text somebody typed must digest it first (CONTRACT.md invariant 6).
const DefaultMaxKeys = 100_000

// New returns a limiter for the policy. A policy with no burst or no interval is
// a programming error and is corrected to the smallest sane value rather than to
// "unlimited".
func New(p Policy) *Limiter {
	return &Limiter{policy: p.sane(), maxKeys: DefaultMaxKeys, buckets: map[string]*bucket{}, order: list.New()}
}

// NewEvicting returns a limiter that, when full, forgets its least recently seen
// key rather than refusing the newcomer.
//
// **Which of the two a caller wants is decided by who can mint keys.** Refusing
// newcomers is right where a key is something an attacker cannot manufacture
// cheaply — and wrong where it can: the guest portal keyed its buckets on a MAC the
// caller *types into the query string*, so one laptop naming `DefaultMaxKeys`
// random MACs filled the table and every real guest after it was refused. A bound
// that turns into a lockout is a denial of service the bound itself provides.
//
// Eviction's cost is the one invariant 3 names: forgetting a key restores its
// burst, so an attacker who can name `maxKeys` fresh keys can reset a bucket. That
// is only a cost where minting keys is cheaper than the budget it resets. A caller
// choosing this limiter says, and a test proves, that its keys are **not** cheap —
// the portal's are the TCP source address of a completed handshake, so resetting
// one bucket means holding a hundred thousand others, each with a budget of its
// own. Keycloak's brute-force detector and nginx's `limit_req_zone` make the same
// trade: a fixed-size zone that drops its oldest state under pressure rather than
// refusing new clients.
func NewEvicting(p Policy) *Limiter {
	l := New(p)
	l.evict = true
	return l
}

func (p Policy) sane() Policy {
	if p.Burst < 1 {
		p.Burst = 1
	}
	if p.Every <= 0 {
		p.Every = time.Second
	}
	return p
}

// SetPolicy changes the bucket shape of a limiter already in use, for a setting an
// operator changed in the console (the admin-configurability rule: a setting takes
// effect without a restart).
//
// **Nobody is handed a fresh burst by it.** Every bucket keeps the tokens it holds,
// refilled to now under the old policy and then capped at the new burst — so
// lowering a limit binds at once, and raising one lets a caller refill towards the
// new ceiling rather than jump to it. Replacing the limiter instead would forget
// every bucket, which is a reset any guesser could trigger by getting an operator
// to press Save.
func (l *Limiter) SetPolicy(p Policy, now time.Time) {
	p = p.sane()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range l.buckets {
		l.refill(b, now)
		if max := float64(p.Burst); b.tokens > max {
			b.tokens = max
		}
	}
	l.policy = p
}

// Policy is the shape in force.
func (l *Limiter) Policy() Policy {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.policy
}

// Allow spends one token from key's bucket. When there is none it spends
// nothing and reports how long until there will be one.
//
// The clock is a parameter so that a test can prove the refill refills: a limiter
// whose recovery has never been exercised might be a permanent lockout.
func (l *Limiter) Allow(key string, now time.Time) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	b, found := l.buckets[key]
	if !found {
		if len(l.buckets) >= l.maxKeys {
			if !l.evict {
				// Full of live keys. Refusing the newcomer rather than evicting an
				// old one: eviction would let an attacker reset any bucket by naming
				// enough fresh keys. The wait quoted is one refill interval.
				return false, l.policy.Every
			}
			// Full, and this limiter's keys are not cheap to mint (NewEvicting):
			// the least recently seen key goes, and the newcomer is admitted.
			if front := l.order.Front(); front != nil {
				l.remove(front.Value.(string))
			}
		}
		b = &bucket{tokens: float64(l.policy.Burst), seen: now}
		b.elem = l.order.PushBack(key)
		l.buckets[key] = b
	} else {
		l.order.MoveToBack(b.elem)
	}
	l.refill(b, now)

	if b.tokens < 1 {
		missing := 1 - b.tokens
		return false, time.Duration(missing * float64(l.policy.Every))
	}
	b.tokens--
	return true, 0
}

// Forget drops a key, which restores its full burst. Used on a successful
// sign-in so that the account's own owner does not carry an attacker's failures.
func (l *Limiter) Forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.remove(key)
}

// remove drops a key from the map and the recency list. The caller holds mu.
func (l *Limiter) remove(key string) {
	if b, ok := l.buckets[key]; ok {
		l.order.Remove(b.elem)
		delete(l.buckets, key)
	}
}

// Len is how many keys are held, for tests and for a metric.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func (l *Limiter) refill(b *bucket, now time.Time) {
	if elapsed := now.Sub(b.seen); elapsed > 0 {
		b.tokens += elapsed.Seconds() / l.policy.Every.Seconds()
		if max := float64(l.policy.Burst); b.tokens > max {
			b.tokens = max
		}
	}
	b.seen = now
}

// sweep drops keys that have refilled completely. It runs at most once per full
// refill period, so its cost is amortised over the requests in between.
func (l *Limiter) sweep(now time.Time) {
	full := time.Duration(l.policy.Burst) * l.policy.Every
	if now.Sub(l.lastSweep) < full {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.seen) >= full {
			l.remove(k)
		}
	}
}

// ClientKey is the bucket key for the peer that sent a request.
//
// The host part of `RemoteAddr`, and for IPv6 the /64 it sits in: a single
// subscriber is routinely handed a whole /64, so keying on the full address lets
// one client rotate through 2^64 buckets. That is the prefix length the large
// rate limiters settle on for the same reason. An address that does not parse is
// keyed on its literal text, which is still one bucket per sender.
func ClientKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return "ip:" + host
	}
	addr = addr.Unmap()
	if addr.Is6() {
		prefix, err := addr.Prefix(64)
		if err == nil {
			return "ip6:" + prefix.String()
		}
	}
	return "ip:" + addr.String()
}
