package store_test

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

// The evidence a CoA driver has instead of a presence endpoint.
//
// RFC 5176 is one-way: you tell a NAS to end or change a session and it answers ACK
// or NAK. There is no query in it at all, so `VerifyPresence` — which `docs/rules/security.md`
// §4 makes mandatory before any grant — has nothing to ask. What is available instead
// is Foyer's own accounting: this MAC authenticated through this NAS and its session
// is still open.
//
// `nas.SessionSource` is declared in `internal/nas` and implemented in
// `internal/apistore`; this is the repository read underneath it.

func seedCoAWorld(t *testing.T, st *store.Store, addr string) (*store.Site, *store.RadiusClient) {
	t.Helper()
	site := seedSite(t, st)
	secret := mustSecret(t, st, site.ID)
	c := &store.RadiusClient{
		ID: uniq("rc"), SiteID: site.ID, Name: "Hotspot router",
		Address: netip.MustParsePrefix(addr + "/32"),
		Source:  "manual", Driver: "mikrotik", SharedSecretID: &secret.ID, Enabled: true,
	}
	fatalf(t, st.RadiusClients().Create(context.Background(), c), "creating the radius client")
	return site, c
}

// TestAnActiveSessionIsFoundHoweverTheNASSpelledTheMAC.
//
// `Calling-Station-Id` has no canonical spelling and vendors disagree — colons,
// dashes, Cisco's dotted quads, bare hex, either case. A source that matched only the
// spelling it was handed would answer *this device is not on the network* for a guest
// standing under the access point, and `docs/rules/security.md` §4 turns that into a refused
// grant rather than a cosmetic miss.
func TestAnActiveSessionIsFoundHoweverTheNASSpelledTheMAC(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()

	spellings := []struct {
		name    string
		written string
	}{
		{"lower colon", "02:aa:bb:01:%02x:01"},
		{"upper dash", "02-AA-BB-01-%02X-01"},
		{"cisco dotted", "02aa.bb01.%02x01"},
		{"bare upper", "02AABB01%02X01"},
	}
	for i, sp := range spellings {
		t.Run(sp.name, func(t *testing.T) {
			site, client := seedCoAWorld(t, st, fmtAddr("10.71.%d.11", i))
			written := fmtMAC(sp.written, i)
			id := uniq("sess")
			now := time.Now()
			seedAccounting(t, site.ID, id, fmtAddr("10.71.%d.11", i), written,
				"94-2A-6F-E1-09-01:foyer-guest", ptrInt64(1), ptrInt64(1), now, now, nil)

			// Asked with the canonical form, whatever the NAS wrote.
			got, err := st.Accounting().ActiveSessionForMAC(ctx, fmtMAC("02:aa:bb:01:%02x:01", i))
			fatalf(t, err, "resolving the session for %s", sp.name)
			if got.AcctSessionID != id {
				t.Errorf("resolved session %q, want %q", got.AcctSessionID, id)
			}
			if got.CallingStationID != written {
				t.Errorf("CallingStationID is %q, want the spelling the NAS actually sent (%q) — "+
					"a driver puts this on the wire and must not be handed a normalised copy",
					got.CallingStationID, written)
			}
			if got.NASIPAddress != fmtAddr("10.71.%d.11", i) {
				t.Errorf("NASIPAddress is %q, want the address the accounting came from", got.NASIPAddress)
			}
			if got.SiteID != site.ID || got.NASID != client.ID {
				t.Errorf("resolved to site %q client %q, want %q / %q",
					got.SiteID, got.NASID, site.ID, client.ID)
			}
		})
	}
}

// TestNoSessionAndCouldNotLookAreDifferentAnswers.
//
// The three-way contract `nas.SessionSource` insists on, and the reason it insists:
// a caller that folds "there is no session" into "I could not look" turns a database
// outage into *the guest's device is not on the network*, which sends somebody to
// toggle Wi-Fi for twenty minutes over a fault the venue can see.
func TestNoSessionAndCouldNotLookAreDifferentAnswers(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site, _ := seedCoAWorld(t, st, "10.72.0.11")

	now := time.Now()
	stopped := now.Add(-time.Minute)
	closedID := uniq("sess")
	seedAccounting(t, site.ID, closedID, "10.72.0.11", "02:aa:bb:02:00:01",
		"94-2A-6F-E1-09-02:foyer-guest", ptrInt64(1), ptrInt64(1),
		now.Add(-time.Hour), now.Add(-time.Hour), &stopped)

	t.Run("a session that has ended is no session", func(t *testing.T) {
		_, err := st.Accounting().ActiveSessionForMAC(ctx, "02:aa:bb:02:00:01")
		if err != store.ErrNotFound {
			t.Fatalf("a closed session answered %v, want ErrNotFound — a device whose "+
				"Acct-Stop has arrived is not on the network", err)
		}
	})

	t.Run("a MAC nobody has seen is no session", func(t *testing.T) {
		_, err := st.Accounting().ActiveSessionForMAC(ctx, "02:aa:bb:02:00:ff")
		if err != store.ErrNotFound {
			t.Fatalf("an unknown MAC answered %v, want ErrNotFound", err)
		}
	})

	t.Run("something that is not a MAC is no session", func(t *testing.T) {
		_, err := st.Accounting().ActiveSessionForMAC(ctx, "not-a-mac")
		if err != store.ErrNotFound {
			t.Fatalf("a non-MAC answered %v, want ErrNotFound", err)
		}
	})

	t.Run("a store that cannot be read is not an answer about the device", func(t *testing.T) {
		// A pool of its own, closed on purpose. The distinction is only provable by
		// making the read fail: with one store for both cases, "could not look"
		// would never be exercised and the branch that carries it would be one more
		// mechanism that looks like a check.
		broken := appStore(t)
		broken.Close()
		_, err := broken.Accounting().ActiveSessionForMAC(ctx, "02:aa:bb:02:00:01")
		if err == nil {
			t.Fatal("a closed store answered without an error; a caller cannot tell an " +
				"outage from an absent device")
		}
		if err == store.ErrNotFound {
			t.Fatal("a closed store answered ErrNotFound; that is *the device is not on the " +
				"network*, said about a database that could not be read")
		}
	})
}

// TestASessionWhoseNASMatchesNoClientIsNotOffered.
//
// The inner join is a refusal, not an accident. A `radacct` row from an address in no
// `clients.conf` entry is the `unknown-radius-client` blind spot: FreeRADIUS
// discarded the request without a reply and the operator has a device Foyer does not
// know. Handing that session to a CoA driver would let it authorize a device on a NAS
// Foyer holds no secret for — and the failure direction that matters here is the one
// that grants access.
func TestASessionWhoseNASMatchesNoClientIsNotOffered(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	now := time.Now()
	seedAccounting(t, site.ID, uniq("sess"), "203.0.113.91", "02:aa:bb:03:00:01",
		"", ptrInt64(1), ptrInt64(1), now, now, nil)

	if _, err := st.Accounting().ActiveSessionForMAC(ctx, "02:aa:bb:03:00:01"); err != store.ErrNotFound {
		t.Fatalf("a session on an unregistered NAS answered %v, want ErrNotFound", err)
	}
}

// TestTheNewestOpenSessionWins.
//
// One MAC can hold more than one open row — a NAS that rebooted without sending
// Acct-Stop leaves the old one open forever, which is the classic RADIUS accounting
// failure and is a documented fixture in this project. The newest is the one a CoA
// request should name; the stale one identifies a session the NAS has forgotten, and
// asking it to change that session is a no-op the device may ACK.
func TestTheNewestOpenSessionWins(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site, _ := seedCoAWorld(t, st, "10.73.0.11")

	now := time.Now()
	stale := uniq("sess")
	seedAccounting(t, site.ID, stale, "10.73.0.11", "02:aa:bb:04:00:01",
		"94-2A-6F-E1-09-04:foyer-guest", ptrInt64(1), ptrInt64(1),
		now.Add(-40*24*time.Hour), now.Add(-40*24*time.Hour), nil)

	live := uniq("sess")
	seedAccounting(t, site.ID, live, "10.73.0.11", "02:aa:bb:04:00:01",
		"94-2A-6F-E1-09-04:foyer-guest", ptrInt64(1), ptrInt64(1), now, now, nil)

	got, err := st.Accounting().ActiveSessionForMAC(ctx, "02:aa:bb:04:00:01")
	fatalf(t, err, "resolving the session")
	if got.AcctSessionID != live {
		t.Fatalf("resolved session %q (the stale one is %q), want the newest — a 40-day-old "+
			"open row is a NAS that rebooted without an Acct-Stop, and a CoA naming it asks "+
			"the device to change a session it has forgotten",
			got.AcctSessionID, stale)
	}
}

func fmtAddr(format string, i int) string { return fmt.Sprintf(format, i) }
func fmtMAC(format string, i int) string  { return fmt.Sprintf(format, i) }
