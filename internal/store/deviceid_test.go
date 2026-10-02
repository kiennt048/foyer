package store_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

// The device id a session names has to be the device id the console hands out.
//
// `ListNas` gives a device the id `coalesce(ap.id, rc.id)` -- the access point's
// where there is one, the clients.conf row's where there is not -- because a `Nas`
// in the contract is a **device**, and twelve access points behind one
// clients.conf row are twelve devices. A `ListSessions` selecting `rc.id` would
// also be right on its own terms: `radacct.nasipaddress` matches a *client*.
//
// Together they would be wrong, and neither query could see it. A device id read
// off `listNas` and sent back as `listSessions?nasId=` would match nothing, and
// `revokeVoucher` would resolve the device it had failed on with a lookup that
// missed for every device that has an access point -- so a failed session end
// would lose the name of the thing it failed on.
//
// These assertions are cross-repository on purpose: each query is self-consistent,
// so only a test that asks both of them can fail.

// seedDeviceWorld gives one site a controller, a secret, and one access point per
// MAC, each with its own clients.conf row derived from its address.
func seedDeviceWorld(t *testing.T, st *store.Store, site *store.Site, macs, addrs []string) {
	t.Helper()
	ctx := context.Background()

	ctl := &store.Controller{
		ID: uniq("ctl"), Name: uniq("Device world"), Vendor: "unifi",
		BaseURL: "https://192.0.2.10:11443", AuthMode: "api-key", TLSMode: "trust-on-first-use",
	}
	fatalf(t, st.Controllers().Create(ctx, ctl, []byte(randomString())), "registering a controller")
	secret := mustSecret(t, st, site.ID)

	devices := make([]store.DiscoveredAP, 0, len(macs))
	for i, mac := range macs {
		devices = append(devices, store.DiscoveredAP{
			MAC: mac, Name: "AP " + mac, IPAddress: addr(t, addrs[i]), State: "online",
		})
	}
	_, err := st.AccessPoints().Reconcile(ctx, ctl.ID, site.ID, secret.ID, "unifi", devices, time.Now())
	fatalf(t, err, "discovering access points")
}

func TestASessionNamesTheDeviceTheConsoleHandsOut(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	const mac = "94:2a:6f:e1:01:01"
	seedDeviceWorld(t, st, site, []string{mac}, []string{"10.81.0.11"})

	ap, err := st.AccessPoints().GetByMAC(ctx, mac)
	fatalf(t, err, "resolving the discovered access point")

	id := uniq("sess")
	now := time.Now()
	seedAccounting(t, site.ID, id, "10.81.0.11", "02:11:22:33:44:01",
		"94-2A-6F-E1-01-01:foyer-guest", ptrInt64(1024), ptrInt64(2048), now, now, nil)

	sess, err := st.Accounting().GetSession(ctx, id)
	fatalf(t, err, "reading the session")

	if sess.NasID != ap.ID {
		t.Errorf("session names device %q; the access point it happened on is %q", sess.NasID, ap.ID)
	}

	// The half that matters to the console: an id `GetNas` can resolve. `GetNas`
	// matches on `coalesce(ap.id, rc.id)`, so a clients.conf id for a client that
	// has an access point matches no row at all.
	device, err := st.RadiusClients().GetNas(ctx, sess.NasID)
	if err != nil {
		t.Fatalf("the device a session names does not resolve through GetNas: %v", err)
	}
	if device.ID != ap.ID {
		t.Errorf("GetNas(%q) resolved device %q", sess.NasID, device.ID)
	}

	// And through the listing, which is where the console actually gets the id it
	// sends back as a filter.
	page, err := st.RadiusClients().ListNas(ctx, store.ListOptions{SiteID: site.ID, Limit: 50})
	fatalf(t, err, "listing devices")
	found := false
	for _, n := range page.Items {
		if n.ID == sess.NasID {
			found = true
		}
	}
	if !found {
		t.Errorf("no device in listNas has the id %q that listSessions reports, so a filter on it matches nothing", sess.NasID)
	}
}

// A MikroTik that is its own NAS has a clients.conf row and no access point, and its
// device id *is* that row's. This is the half a naive `ap.id` would break, and it is
// why the column is a coalesce rather than a replacement.
func TestAClientWithNoAccessPointIsItsOwnDevice(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	secret := mustSecret(t, st, site.ID)

	client := &store.RadiusClient{
		ID: uniq("rc"), SiteID: site.ID, Name: "Border router",
		Address: netip.MustParsePrefix("10.82.0.1/32"),
		Source:  "manual", Driver: "mikrotik", SharedSecretID: &secret.ID, Enabled: true,
	}
	fatalf(t, st.RadiusClients().Create(ctx, client), "creating a client with no access point")

	id := uniq("sess")
	now := time.Now()
	seedAccounting(t, site.ID, id, "10.82.0.1", "02:11:22:33:44:02", "", ptrInt64(1), ptrInt64(1), now, now, nil)

	sess, err := st.Accounting().GetSession(ctx, id)
	fatalf(t, err, "reading the session")
	if sess.NasID != client.ID {
		t.Errorf("session names device %q, want the clients.conf row %q", sess.NasID, client.ID)
	}
	if _, err := st.RadiusClients().GetNas(ctx, sess.NasID); err != nil {
		t.Fatalf("the device a session names does not resolve through GetNas: %v", err)
	}
}

// A row that does not name a MAC still resolves when there is only one thing it
// could be, and refuses to guess when there is not.
//
// Two halves in one test on purpose: a fix that resolved everything to some access
// point would pass the first and fail the second, and a fix that resolved nothing
// would pass the second and fail the first.
func TestAnUnnamedAccessPointResolvesOnlyWhenItIsUnambiguous(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	secret := mustSecret(t, st, site.ID)
	now := time.Now()

	t.Run("one access point behind the client", func(t *testing.T) {
		seedDeviceWorld(t, st, site, []string{"94:2a:6f:e1:02:01"}, []string{"10.83.0.11"})
		ap, err := st.AccessPoints().GetByMAC(ctx, "94:2a:6f:e1:02:01")
		fatalf(t, err, "resolving the access point")

		// No Called-Station-Id at all: plenty of NAS send none.
		id := uniq("sess")
		seedAccounting(t, site.ID, id, "10.83.0.11", "02:11:22:33:44:03", "", ptrInt64(1), ptrInt64(1), now, now, nil)

		sess, err := st.Accounting().GetSession(ctx, id)
		fatalf(t, err, "reading the session")
		if sess.NasID != ap.ID {
			t.Errorf("session names %q; the client has exactly one access point, %q", sess.NasID, ap.ID)
		}
		if _, err := st.RadiusClients().GetNas(ctx, sess.NasID); err != nil {
			t.Fatalf("the device a session names does not resolve through GetNas: %v", err)
		}
	})

	t.Run("a fleet behind one clients.conf row does not guess", func(t *testing.T) {
		fleet := &store.RadiusClient{
			ID: uniq("rc"), SiteID: site.ID, Name: "Fleet proxy",
			Address: netip.MustParsePrefix("10.84.0.0/24"),
			Source:  "manual", Driver: "coa", SharedSecretID: &secret.ID, Enabled: true,
		}
		fatalf(t, st.RadiusClients().Create(ctx, fleet), "creating the fleet client")

		ctl := &store.Controller{
			ID: uniq("ctl"), Name: uniq("Proxying controller"), Vendor: "omada",
			BaseURL: "https://192.0.2.11", AuthMode: "api-key", TLSMode: "trust-on-first-use",
		}
		fatalf(t, st.Controllers().Create(ctx, ctl, []byte(randomString())), "registering a controller")
		_, err := st.AccessPoints().Reconcile(ctx, ctl.ID, site.ID, secret.ID, "coa", []store.DiscoveredAP{
			{MAC: "94:2a:6f:e1:03:01", Name: "AP one", RadiusVia: "controller", State: "online"},
			{MAC: "94:2a:6f:e1:03:02", Name: "AP two", RadiusVia: "controller", State: "online"},
		}, now)
		fatalf(t, err, "discovering two proxied access points")

		for _, mac := range []string{"94:2a:6f:e1:03:01", "94:2a:6f:e1:03:02"} {
			ap, err := st.AccessPoints().GetByMAC(ctx, mac)
			fatalf(t, err, "resolving %s", mac)
			fatalf(t, st.AccessPoints().AttachRadiusClient(ctx, ap.ID, &fleet.ID), "attaching %s", mac)
		}

		id := uniq("sess")
		seedAccounting(t, site.ID, id, "10.84.0.7", "02:11:22:33:44:04", "", ptrInt64(1), ptrInt64(1), now, now, nil)

		sess, err := st.Accounting().GetSession(ctx, id)
		fatalf(t, err, "reading the session")
		if sess.NasID != fleet.ID {
			t.Errorf("session names %q; with two access points behind one address there is nothing to name but the client %q", sess.NasID, fleet.ID)
		}
	})
}
