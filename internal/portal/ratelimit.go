package portal

import (
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/ratelimit"
)

// The pre-authentication rate limit.
//
// §5 lists three bounds on the pre-auth state — byte cap, rate limit, time limit
// — and says why there have to be three: "There is no identity yet, so nothing
// else bounds it." The byte cap belongs to the walled garden and is enforced in
// the data plane; the time limit is `Session.ExpiresAt`; this is the rate limit,
// and it is the one this package both can and must enforce.
//
// What it is actually defending, in order of how much it matters:
//
//  1. **The venue's controller.** Every presence check and every grant is an HTTP
//     call to somebody's UniFi. An unlimited portal is a way for one laptop to
//     take a venue's controller down, and the venue would read that as Foyer
//     being broken.
//  2. **The voucher space.** Twelve characters from a 31-character alphabet is a
//     little over 59 bits, so guessing is not the threat — but `WHERE code = $1`
//     against an indexed column is not a constant-time comparison and cannot be
//     made one, so the rate in front of it is what stands in for the timing
//     property `code.go` can only provide in memory.
//  3. **Account passwords.** The portal's account login is reachable by anyone
//     within radio range and argon2id is deliberately slow, which makes an
//     unlimited login endpoint a way to spend the venue's CPU as well as guess.

// TokenBucket is an in-process limiter: a fixed budget per key, refilled at a
// steady rate.
//
// **It is in-process, and that is a stated limit rather than an oversight.** A
// deployment running two control-plane replicas gets twice the configured rate,
// and nothing here would say so. That is why `Limiter` is a port: the replacement
// is a shared counter, and the interface is what lets it be dropped in without
// touching a handler. Single-process is the shape v1 ships and the honest reading
// of what this enforces.
//
// **It is `internal/ratelimit.Limiter` under this package's port**, and it is
// **two** of them, one per key family (see KeySource
// and KeyDevice): a flood-sized bucket per connection source, spent before
// `VerifyPresence`, and the guest-sized bucket `Burst`/`Every` describe per
// verified device, spent after it. Both evict their least recently seen key when
// full rather than refusing a newcomer (`ratelimit.NewEvicting`), because a full
// table that refuses newcomers is a lockout.
type TokenBucket struct {
	device *ratelimit.Limiter
	source *ratelimit.Limiter

	// Burst is how many attempts one verified device may make back to back. A
	// guest who mistypes a twelve-character code three times in a row is
	// ordinary; the number is set so that nothing a person does by hand reaches
	// it.
	Burst int
	// Every is how long one token takes to come back.
	Every time.Duration
}

// The two key families, and why a key says which it is.
//
// ── what keying on the MAC alone gets wrong ───────────────────────────────────
//
// A bucket keyed on `sess.ClientMAC` — the MAC the caller typed into the query
// string — and spent **before** the presence check lets a laptop within radio
// range name `ratelimit.DefaultMaxKeys` random MACs, each a fresh bucket with a
// fresh burst, until the table is full; past that, the limiter refuses every new
// key, and every real guest who arrives afterwards is told *something went
// wrong*. The bound on memory becomes the denial of service. And the per-MAC key
// does not bound the thing it is for either: a caller rotating MACs gets a fresh
// burst of controller calls per MAC.
//
// ── what the field does ─────────────────────────────────────────────────────
//
// PacketFence throttles its captive portal per **client IP** (`captive_portal.
// rate_limiting`, a per-IP hit counter checked before any lookup), and keys the
// device only once it has been identified. nginx's `limit_req_zone` — which is what
// sits in front of most portals — keys on `$binary_remote_addr` and, when its zone
// is full, drops its oldest state rather than refusing clients. OWASP's
// credential-stuffing cheat sheet says the same thing in general form: throttle on
// what the client cannot choose, and never let the throttle itself become the
// lockout.
//
// ── what this does ──────────────────────────────────────────────────────────
//
//   - **Before presence, the connection source** (`KeySource`): the TCP peer
//     address, an IPv6 peer on its /64 (`ratelimit.ClientKey`). A key costs a
//     completed handshake from an address, so a flood of claimed MACs from one
//     laptop is one bucket, and that bucket is what bounds the controller calls.
//     It is sized as a **flood bound** — `SourceShare` guests' worth — because a
//     venue whose portal is reached through one NAT presents every guest as one
//     address, and a per-guest size there would be a venue-wide cap (the reason
//     `internal/identity/otp` sizes its address limit the same way).
//   - **After presence, the device on that source** (`KeyDevice`): the MAC the
//     controller has just confirmed, together with the source it was claimed from.
//     Only a MAC the controller vouched for ever gets a bucket, so the table is
//     bounded by the devices really on the network; and the source is part of the
//     key so that somebody claiming a present guest's MAC from their own laptop
//     spends their own budget, not the guest's.
const (
	KeySource = "src:"
	KeyDevice = "dev:"
)

// SourceShare is how many guests' worth of budget one connection source gets
// before presence — the flood bound a venue behind one NAT still fits inside.
const SourceShare = 10

// DefaultBurst and DefaultRefill are what `internal/api` uses when a deployment
// says nothing.
const (
	DefaultBurst  = 10
	DefaultRefill = 6 * time.Second
)

var _ SharedLimiter = (*TokenBucket)(nil)

// SharedAcrossProcesses is false, and it is written down rather than left to the
// absence of a method.
//
// The absence would work — `limiterIsShared` defaults to false — but it would make
// this type's most consequential property invisible at the place somebody reads it.
// A second control-plane replica running this limiter grants twice the configured
// pre-auth rate, and the only thing that turns that from a silent doubling into a
// refusal is a deployment declaring its replica count and this answering honestly.
func (t *TokenBucket) SharedAcrossProcesses() bool { return false }

// NewTokenBucket returns a limiter with the defaults filled in. The device family
// gets `burst` every `every`; the source family `SourceShare` times as much, over
// the same refill period.
func NewTokenBucket(burst int, every time.Duration) *TokenBucket {
	if burst < 1 {
		burst = DefaultBurst
	}
	if every <= 0 {
		every = DefaultRefill
	}
	return &TokenBucket{
		device: ratelimit.NewEvicting(ratelimit.Policy{Burst: burst, Every: every}),
		source: ratelimit.NewEvicting(ratelimit.Policy{Burst: burst * SourceShare, Every: every / SourceShare}),
		Burst:  burst,
		Every:  every,
	}
}

// Allow consumes one token, or refuses. A `KeySource` key is spent from the
// source family; every other key from the device family.
//
// The clock is a parameter rather than `time.Now()` so that a test can prove the
// refill actually refills. A limiter whose recovery has never been exercised is a
// limiter that might be a permanent lockout, and locking a café's guests out of
// the network for the rest of the day is a worse failure than the one this
// prevents.
func (t *TokenBucket) Allow(key string, at time.Time) bool {
	l := t.device
	if strings.HasPrefix(key, KeySource) {
		l = t.source
	}
	ok, _ := l.Allow(key, at)
	return ok
}

// Len is how many keys each family holds, for a test that proves a flood of
// unverified claims creates no device buckets.
func (t *TokenBucket) Len() (source, device int) {
	return t.source.Len(), t.device.Len()
}
