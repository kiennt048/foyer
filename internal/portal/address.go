package portal

import (
	"container/list"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// Which portal session was last seen from which client address — the one thing
// the RFC 8908 endpoint needs and the cookie cannot give it.
//
// ── why a second key, when the cookie is the only key everywhere else ─────────
//
// Every other reader in this package resolves a browser through its cookie and
// nothing else (`SessionStore` has no lookup by MAC, on purpose). The RFC 8908
// client is **not a browser**: it is the operating system's own captive-portal
// API client — Android's `NetworkMonitor`, iOS's `CaptiveNetworkSupport` — and it
// carries no cookie from the portal's WebView, whose data store is ephemeral
// anyway. So the API answered `captive: true` to every device, always, including
// every device that had just been let on.
//
// RFC 8908 §4 names the mechanism it expects: the API server and the enforcement
// device must see *"the same IP addresses"*, and where that is not enough the
// provisioned URI should be distinct per client. Foyer does not provision the URI
// — DHCP option 114 is configured on the venue's gateway, one value for the whole
// SSID — so the source address is what is left, and it is what this records.
//
// ── what it is keyed on, and why that is not the MAC mistake again ───────────
//
// The address is the **transport source of a TCP connection that completed**, not
// a parameter. `X-Forwarded-For` is never read: it is a header the caller types.
// To be recorded against an address a client has to have completed a handshake
// from it *and* resolved a session — which is the front door, the API middleware
// or a document render, all of them paths where the session came from the cookie
// or from a fresh mint.
//
// What an entry lets anybody learn is small and is stated rather than waved at:
// whether the device at that address is captive, and how many seconds its grant
// has left. Never its MAC, its voucher, its plan name or its session token.
//
// ── the two ways an address stops meaning one device ─────────────────────────
//
//   - **NAT between the guests and the portal.** Every guest then arrives from one
//     address. The first time a second MAC is seen from an address inside the
//     window, the entry is marked **ambiguous** and answers as though it were
//     unknown — `captive: true`, the safe answer for a device the portal cannot
//     tell apart. A deployment behind NAT therefore gets the answer an
//     address-blind endpoint would give, never another guest's grant.
//   - **DHCP handing a departed guest's address to a new device** while the old
//     grant is still live. Until the new device reaches the front door, its
//     operating system is told `captive: false`. That is the residual risk of
//     keying on an address at all, it is RFC 8908's own stated precondition, and
//     it is bounded by the DHCP server's reluctance to reuse a leased address
//     within the lease. `docs/portal-https.md` says so to the operator.
//
// ── in-process, and failing towards the old answer ───────────────────────────
//
// It is memory in one process, like `TokenBucket`, and unlike `TokenBucket` that is
// **not refused** for a multi-replica deployment: a replica that never saw the
// front door answers `captive: true`, which is the pre-existing answer, while an
// in-process rate limiter fails in the permissive direction. A restart forgets
// every entry for the same reason and with the same outcome.

// AddressBook remembers the session last seen from a client address.
//
// A port, like `Limiter`, so that a deployment with more than one control-plane
// process can supply a shared one. `NewService` supplies `NewAddressBook()` when
// none is wired.
type AddressBook interface {
	// Observe records that a browser at addr resolved this session. grantID may
	// be empty — the session has not been granted yet.
	Observe(addr netip.Addr, tokenHash, clientMAC, grantID string, at time.Time)
	// AttachGrant records a grant against every address that session was seen
	// from. It is how an entry outlives the session row: a pre-authentication
	// session expires after thirty minutes and a grant can last all day.
	AttachGrant(tokenHash, grantID string, at time.Time)
	// Lookup returns the entry for addr, if one is live.
	Lookup(addr netip.Addr, at time.Time) (AddressEntry, bool)
}

// AddressEntry is what the book knows about one address.
type AddressEntry struct {
	TokenHash string
	ClientMAC string
	GrantID   string
	// Ambiguous means more than one MAC has been seen from this address inside
	// the window. The reader must treat the address as unknown.
	Ambiguous bool
}

// DefaultAddressWindow is how long an observation keeps an address meaningful.
//
// A day, because a grant is commonly an afternoon or a day and the operating
// system keeps asking for as long as it is on the network; after it, the answer
// goes back to `captive: true`, which is the pre-existing answer. The window
// restarts on every observation, and the operating system's own API queries are
// **not** observations — they carry nothing that proves who is asking.
const DefaultAddressWindow = 24 * time.Hour

// DefaultAddressCapacity bounds the book. At roughly 200 bytes an entry it is a
// few megabytes at most, inside a control plane with a 256 MiB heap limit. An
// address evicted by pressure answers `captive: true` again.
const DefaultAddressCapacity = 16384

type addressRecord struct {
	AddressEntry
	addr           netip.Addr
	seen           time.Time
	ambiguousUntil time.Time
	elem           *list.Element // in MemoryAddressBook.order
}

// MemoryAddressBook is the in-process AddressBook.
type MemoryAddressBook struct {
	mu       sync.Mutex
	window   time.Duration
	capacity int
	byAddr   map[netip.Addr]*addressRecord
	// order holds every record, seen longest ago at the front. It is what keeps
	// an insert into a full book O(1): the address that decides the book's size
	// is the transport source, and on IPv6 a single guest holding one cookie can
	// arrive from as many of those as it likes — a linear scan under this lock on
	// every new one is a cost the guest chooses.
	order *list.List
}

var _ AddressBook = (*MemoryAddressBook)(nil)

// NewAddressBook returns an empty in-process book with the default bounds.
func NewAddressBook() *MemoryAddressBook {
	return NewAddressBookWith(DefaultAddressWindow, DefaultAddressCapacity)
}

// NewAddressBookWith returns an empty book with explicit bounds, for a test that
// has to reach them.
func NewAddressBookWith(window time.Duration, capacity int) *MemoryAddressBook {
	if window <= 0 {
		window = DefaultAddressWindow
	}
	if capacity <= 0 {
		capacity = DefaultAddressCapacity
	}
	return &MemoryAddressBook{window: window, capacity: capacity, byAddr: map[netip.Addr]*addressRecord{}, order: list.New()}
}

func (b *MemoryAddressBook) Observe(addr netip.Addr, tokenHash, clientMAC, grantID string, at time.Time) {
	if !addr.IsValid() || tokenHash == "" || clientMAC == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	rec, ok := b.byAddr[addr]
	if ok && !b.live(rec, at) {
		b.remove(rec)
		ok = false
	}
	if !ok {
		b.makeRoom(at)
		rec = &addressRecord{addr: addr}
		rec.elem = b.order.PushBack(rec)
		b.byAddr[addr] = rec
	} else {
		b.order.MoveToBack(rec.elem)
	}
	if ok && rec.ClientMAC != clientMAC {
		// A second device behind one address. Not re-pointed and not guessed at:
		// the address stops meaning anybody until the window has passed with no
		// further conflict — and the grant the other device held is not this
		// one's.
		rec.ambiguousUntil = at.Add(b.window)
		rec.GrantID = ""
	}
	// A new session for the **same** MAC keeps the grant. That is the ordinary
	// case rather than an edge: the CNA destroys its cookies when its window
	// closes, so a guest who is online and meets the portal again arrives with no
	// cookie and is minted a fresh, grantless session. The grant is bound to the
	// MAC and the reader re-checks that, so dropping it here would only tell an
	// online device it is captive.
	rec.TokenHash = tokenHash
	rec.ClientMAC = clientMAC
	if grantID != "" {
		rec.GrantID = grantID
	}
	rec.seen = at
}

func (b *MemoryAddressBook) AttachGrant(tokenHash, grantID string, at time.Time) {
	if tokenHash == "" || grantID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, rec := range b.byAddr {
		if rec.TokenHash == tokenHash && b.live(rec, at) {
			rec.GrantID = grantID
		}
	}
}

func (b *MemoryAddressBook) Lookup(addr netip.Addr, at time.Time) (AddressEntry, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec, ok := b.byAddr[addr]
	if !ok {
		return AddressEntry{}, false
	}
	if !b.live(rec, at) {
		b.remove(rec)
		return AddressEntry{}, false
	}
	out := rec.AddressEntry
	out.Ambiguous = at.Before(rec.ambiguousUntil)
	return out, true
}

// Len is how many addresses are held, for a test asserting the bound.
func (b *MemoryAddressBook) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.byAddr)
}

func (b *MemoryAddressBook) live(rec *addressRecord, at time.Time) bool {
	return at.Before(rec.seen.Add(b.window))
}

func (b *MemoryAddressBook) remove(rec *addressRecord) {
	b.order.Remove(rec.elem)
	delete(b.byAddr, rec.addr)
}

// makeRoom drops expired entries from the front of the order and then, if the
// book is still full, the entry seen longest ago. Called with the lock held.
// Amortised O(1): each record is removed at most once. The order is by
// observation, and observations carry the service clock, so a clock stepped
// backwards makes the eviction pick a near-oldest entry rather than the oldest —
// the only cost is an address answering `captive: true` a little early.
func (b *MemoryAddressBook) makeRoom(at time.Time) {
	for front := b.order.Front(); front != nil; front = b.order.Front() {
		rec := front.Value.(*addressRecord)
		if len(b.byAddr) < b.capacity && b.live(rec, at) {
			return
		}
		b.remove(rec)
	}
}

// ClientAddress is the transport source of the request, and nothing else.
//
// **`X-Forwarded-For` and `Forwarded` are deliberately not read.** They are
// headers the caller writes, and an address the caller writes is the MAC mistake
// again. A deployment that puts a proxy between the guests and the portal loses
// per-device answers — every request arrives from the proxy, the book marks that
// address ambiguous, and the endpoint answers `captive: true` — which is the safe
// direction to lose in.
func ClientAddress(r *http.Request) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap().WithZone(""), true
}
