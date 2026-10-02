package apistore

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/kiennt048/foyer/internal/portal"
	"github.com/kiennt048/foyer/internal/store"
)

// The RFC 8908 address book over PostgreSQL (`portal_addresses`, migration 00045).
//
// **The database is the book; the process keeps a copy only to write less.**
// `portal.MemoryAddressBook` answers from one process's memory, so a restart or a
// second control-plane process would tell every online device `captive: true`
// until it next met the front door. Here:
//
//   - `Lookup` reads the table, so every process answers from the same entry — and
//     an address two processes each saw a different MAC from is ambiguous in both;
//   - `Observe` writes when the entry materially changes (a new address, session,
//     device or grant, or an ambiguity) and otherwise at most once per
//     `addressRefresh`, because every portal request observes and a write per
//     request is a write per page view. The cost of the throttle is that another
//     process may forget an idle-but-live entry up to `addressRefresh` early —
//     `captive: true`, the fail-safe direction;
//   - **anything the database cannot answer is the fail-safe**: a failed read is
//     answered from this process's copy, and a miss in both is `captive: true`,
//     exactly as the in-memory book answers.
//
// Personal data: the MAC and the address. Erased by MAC and with an account's
// grants (store registry), and reaped a window after last seen by the portal-session
// job. This process's copy holds the same for at most the window and is never
// returned to a caller; the grant a stale copy points at is re-read and re-checked
// against the MAC on every answer, so an erased guest's grant cannot be answered
// from it.
type addressBook struct {
	a      *Adapter
	mem    *portal.MemoryAddressBook
	window time.Duration

	mu      sync.Mutex
	written map[netip.Addr]writtenEntry
}

type writtenEntry struct {
	entry portal.AddressEntry
	at    time.Time
}

// addressRefresh bounds how stale the table's `seen_at` may be for an entry that has
// not changed: an hour against a day's window.
const addressRefresh = time.Hour

// addressTimeout bounds one statement. The caller is a guest's page or the
// operating system's API query, neither of which should wait on a slow database
// for an answer whose fallback is already correct.
const addressTimeout = 2 * time.Second

func newAddressBook(a *Adapter) *addressBook {
	return &addressBook{a: a, mem: portal.NewAddressBook(), window: portal.DefaultAddressWindow,
		written: map[netip.Addr]writtenEntry{}}
}

var _ portal.AddressBook = (*addressBook)(nil)

func (b *addressBook) Observe(addr netip.Addr, tokenHash, clientMAC, grantID string, at time.Time) {
	if !addr.IsValid() || tokenHash == "" || clientMAC == "" {
		return
	}
	b.mem.Observe(addr, tokenHash, clientMAC, grantID, at)
	now, _ := b.mem.Lookup(addr, at)

	b.mu.Lock()
	last, ok := b.written[addr]
	due := !ok || last.entry != now || at.Sub(last.at) >= addressRefresh
	b.mu.Unlock()
	if !due {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), addressTimeout)
	defer cancel()
	if err := b.a.st.PortalAddresses().Observe(ctx, addr, tokenHash, clientMAC, grantID, at, b.window); err != nil {
		slog.Warn("portal: the address book could not be written; this address answers from this process only",
			"error", err.Error())
		return
	}
	b.mu.Lock()
	if len(b.written) >= portal.DefaultAddressCapacity {
		// Forgetting what was written costs a write, never a wrong answer.
		clear(b.written)
	}
	b.written[addr] = writtenEntry{entry: now, at: at}
	b.mu.Unlock()
}

func (b *addressBook) AttachGrant(tokenHash, grantID string, at time.Time) {
	if tokenHash == "" || grantID == "" {
		return
	}
	b.mem.AttachGrant(tokenHash, grantID, at)
	ctx, cancel := context.WithTimeout(context.Background(), addressTimeout)
	defer cancel()
	if err := b.a.st.PortalAddresses().AttachGrant(ctx, tokenHash, grantID, at, b.window); err != nil {
		slog.Warn("portal: a grant could not be attached in the address book", "error", err.Error())
	}
}

func (b *addressBook) Lookup(addr netip.Addr, at time.Time) (portal.AddressEntry, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), addressTimeout)
	defer cancel()
	e, err := b.a.st.PortalAddresses().Lookup(ctx, addr, at, b.window)
	switch {
	case err == nil:
		return portal.AddressEntry{TokenHash: e.TokenHash, ClientMAC: e.ClientMAC,
			GrantID: e.GrantID, Ambiguous: e.Ambiguous}, true
	case isNotFound(err):
		return portal.AddressEntry{}, false
	default:
		// The table could not be read: this process's own copy, which is the book
		// as it was before the table existed.
		return b.mem.Lookup(addr, at)
	}
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
