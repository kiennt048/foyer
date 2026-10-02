package store_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// TestRADIUSVoucherIsClaimedOnlyAtItsOwnSite is the SQL half of the RADIUS site
// binding. internal/aaa's TestAVoucherFromOneSiteIsRefusedAtAnother
// is the engine half.
//
// A Policy API that claimed with siteID "", which `GrantRepo.ClaimVoucher` reads
// as "any venue", would let venue B's access points accept a code printed for
// venue A. Against such code this is red: the claim at B succeeds and spends A's
// voucher.
func TestRADIUSVoucherIsClaimedOnlyAtItsOwnSite(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	siteA, siteB := seedSite(t, st), seedSite(t, st)

	code, err := identity.GenerateVoucherCode(12)
	fatalf(t, err, "generating a code")
	batch := &store.VoucherBatch{
		ID: uniq("bat"), SiteID: siteA.ID, Label: "Site A", Count: 1, ExpiryMode: "from-first-use",
		Plan: store.Plan{DurationMinutes: ptrInt(60), EnforcedBy: "nas"},
	}
	fatalf(t, st.Vouchers().CreateBatch(ctx, batch, []store.NewVoucher{{ID: uniq("v"), Code: code}}), "creating the batch")

	info, err := st.LookupVoucher(ctx, code)
	fatalf(t, err, "looking the voucher up")
	if info.SiteID != siteA.ID {
		t.Fatalf("LookupVoucher says site %q, want %q: the engine cannot refuse a foreign code it cannot place", info.SiteID, siteA.ID)
	}

	now := time.Now()
	if _, err := st.ClaimVoucher(ctx, siteB.ID, code, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claiming site A's voucher at site B = %v, want ErrNotFound", err)
	}
	if _, err := st.ClaimVoucher(ctx, "", code, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claiming with no site = %v, want ErrNotFound: an empty site is every site", err)
	}
	claimed, err := st.ClaimVoucher(ctx, siteA.ID, code, now)
	fatalf(t, err, "claiming at site A — the refusals above must not have spent it")
	if claimed.SiteID != siteA.ID {
		t.Errorf("claimed voucher reports site %q", claimed.SiteID)
	}
}

// TestRADIUSClientSiteIsTheClientFreeRADIUSMatched: the most specific enabled
// client wins, as it does in FreeRADIUS's own lookup, and an address no enabled
// client covers has no site.
func TestRADIUSClientSiteIsTheClientFreeRADIUSMatched(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	fleetSite, terraceSite := seedSite(t, st), seedSite(t, st)
	secret := mustSecret(t, st, fleetSite.ID)

	// A /24 at one venue with a /32 inside it at another. Random octets keep this
	// test clear of every other client in the shared database.
	base := netip.AddrFrom4([4]byte{10, 200, byte(time.Now().UnixNano() % 250), 0})
	fleet := &store.RadiusClient{
		ID: uniq("rc"), SiteID: fleetSite.ID, Name: "Fleet", Address: netip.PrefixFrom(base, 24),
		Source: "manual", Driver: "unifi", SharedSecretID: &secret.ID, Enabled: true,
	}
	fatalf(t, st.RadiusClients().Create(ctx, fleet), "creating the fleet client")
	terraceAddr := netip.AddrFrom4([4]byte{10, 200, base.As4()[2], 9})
	terrace := &store.RadiusClient{
		ID: uniq("rc"), SiteID: terraceSite.ID, Name: "Fleet", Address: netip.PrefixFrom(terraceAddr, 32),
		Source: "manual", Driver: "unifi", SharedSecretID: &secret.ID, Enabled: true,
	}
	fatalf(t, st.RadiusClients().Create(ctx, terrace), "creating the terrace client")

	inside := netip.AddrFrom4([4]byte{10, 200, base.As4()[2], 4}).String()
	if got, err := st.RADIUSClientSite(ctx, inside); err != nil || got != fleetSite.ID {
		t.Errorf("an address inside the /24 resolved to %q, %v; want the fleet's site", got, err)
	}
	if got, err := st.RADIUSClientSite(ctx, terraceAddr.String()); err != nil || got != terraceSite.ID {
		t.Errorf("the /32 resolved to %q, %v; want the terrace's site — the more specific client "+
			"is the one FreeRADIUS verified the packet against", got, err)
	}
	for _, bad := range []string{"", "not-an-address", "192.0.2.77"} {
		if got, err := st.RADIUSClientSite(ctx, bad); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("RADIUSClientSite(%q) = %q, %v; want ErrNotFound", bad, got, err)
		}
	}

	pool := ownerPool(t)
	if _, err := pool.Exec(ctx, `UPDATE radius_clients SET enabled = false WHERE id = $1`, terrace.ID); err != nil {
		t.Fatalf("disabling: %v", err)
	}
	if got, err := st.RADIUSClientSite(ctx, terraceAddr.String()); err != nil || got != fleetSite.ID {
		t.Errorf("with the /32 disabled its address resolved to %q, %v; FreeRADIUS no longer loads it, "+
			"so the /24 is the client that matched", got, err)
	}
}
