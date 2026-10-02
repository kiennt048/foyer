package apistore_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/store"
)

// The seam that did not exist, and what it can and cannot build.
//
// `Drivers.ForController` is right for the vendors that have a controller and cannot
// serve the CoA family at all: `internal/nas` says *"there is nothing to discover;
// the device is its own controller"*, and the contract agrees from the other side —
// `ControllerVendor` is `[unifi, omada, ruckus]` while `NasDriverKind` also has
// `mikrotik` and `coa`. So a MikroTik registered by hand had a `radius_clients` row,
// a secret, a CoA port and a driver in `internal/nas`, and no way for the API to
// build one.

// TestVendorDriversBuildWhatTheyCanAndRefuseTheRest.
//
// Three branches and three different refusals, because "Foyer will not drive this"
// and "Foyer cannot tell which vendor this is" are different problems for different
// people, and one of them is one schema column away from being solved.
func TestVendorDriversBuildWhatTheyCanAndRefuseTheRest(t *testing.T) {
	client := func(driver string) store.RadiusClient {
		return store.RadiusClient{
			ID: "rc-1", SiteID: "site-1", Name: "Hotspot router", Driver: driver,
			Address: netip.MustParsePrefix("10.90.0.11/32"),
		}
	}

	t.Run("mikrotik builds a CoA driver", func(t *testing.T) {
		d, err := apistore.VendorDrivers{}.ForNAS(client("mikrotik"), "s3cret", nil)
		if err != nil {
			t.Fatalf("building the MikroTik driver: %v", err)
		}
		if _, ok := d.(*nas.CoADriver); !ok {
			t.Fatalf("built a %T, want *nas.CoADriver", d)
		}
		if got := d.Capabilities(); got.HardQuota {
			t.Errorf("the MikroTik driver claims HardQuota; its capabilities must be the "+
				"vendor's own, not a default (%+v)", got)
		}
	})

	t.Run("a CoA driver with no session source confirms nothing", func(t *testing.T) {
		// The fail-closed property, exercised rather than described. RFC 5176 has no
		// query in it, so a driver with no accounting behind it has no way to say
		// *no* — and a driver that answered `true` because it could not say no is
		// the portal-as-a-public-API-for-authorizing-arbitrary-MACs failure
		// `docs/rules/security.md` §4 is entirely about.
		d, err := apistore.VendorDrivers{}.ForNAS(client("mikrotik"), "s3cret", nil)
		if err != nil {
			t.Fatalf("building the MikroTik driver: %v", err)
		}
		ok, err := d.VerifyPresence(context.Background(), "02:aa:bb:00:00:01", "", "")
		if ok {
			t.Fatal("a CoA driver with no session source confirmed a device present")
		}
		if !errors.Is(err, nas.ErrNoSessionSource) {
			t.Errorf("the refusal is %v, want ErrNoSessionSource — a bare false would read "+
				"as *this device is not associated*", err)
		}
	})

	// ── one driver kind, two vendors ─────────────────────────────────────────
	//
	// `driver: coa` is one enum member for two vendors, and `radius_clients.
	// coa_vendor` (migration 00015) is what says which. Both halves are asserted
	// here and the refusal is the half that matters: it is the only one whose
	// failure mode is invisible, because a CoA request naming a session in the
	// wrong spelling is answered by a careless NAS with an ACK and no effect.
	t.Run("coa builds the vendor the row names", func(t *testing.T) {
		for _, c := range []struct {
			vendor string
			want   nas.Vendor
		}{
			{"cisco", nas.VendorCisco},
			{"aruba", nas.VendorAruba},
		} {
			row := client("coa")
			row.CoAVendor = ptr(c.vendor)
			d, err := apistore.VendorDrivers{}.ForNAS(row, "s3cret", nil)
			if err != nil {
				t.Fatalf("building the %s driver: %v", c.vendor, err)
			}
			if _, ok := d.(*nas.CoADriver); !ok {
				t.Fatalf("%s built a %T, want *nas.CoADriver", c.vendor, d)
			}
			// The vendor and not merely *a* driver. The two are the same Go type
			// and differ only in the value inside them, so a mix-up here is a
			// driver that builds, connects, sends and matches nothing.
			if got := d.Vendor(); got != c.want {
				t.Errorf("coa_vendor %q built a %s driver, want %s — the two identify a "+
					"session differently and the wrong one is ACKed with no effect",
					c.vendor, got, c.want)
			}
		}
	})

	t.Run("coa refuses when the row records no vendor", func(t *testing.T) {
		_, err := apistore.VendorDrivers{}.ForNAS(client("coa"), "s3cret", nil)
		if !errors.Is(err, apistore.ErrCoAVendorUnknown) {
			t.Fatalf("driver 'coa' with no vendor answered %v, want ErrCoAVendorUnknown", err)
		}
		// The refusal has to name the gap and the row: Cisco and Aruba are both
		// implemented in `internal/nas`, so this is a fact about one device rather
		// than about the drivers, and an operator has to know which device.
		for _, want := range []string{"Cisco", "Aruba", "Hotspot router", "rc-1"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not mention %s: %q", want, err)
			}
		}
	})

	t.Run("coa refuses a vendor nobody here knows", func(t *testing.T) {
		row := client("coa")
		row.CoAVendor = ptr("juniper")
		d, err := apistore.VendorDrivers{}.ForNAS(row, "s3cret", nil)
		if !errors.Is(err, apistore.ErrCoAVendorUnknown) {
			t.Fatalf("coa_vendor \"juniper\" answered %v, want ErrCoAVendorUnknown", err)
		}
		if d != nil {
			t.Fatalf("an unrecognised vendor still produced a %T", d)
		}
		if !strings.Contains(err.Error(), "juniper") {
			t.Errorf("the refusal does not name the value it could not read: %q", err)
		}
	})

	t.Run("a controller vendor is refused here", func(t *testing.T) {
		for _, driver := range []string{"unifi", "omada", "ruckus"} {
			_, err := apistore.VendorDrivers{}.ForNAS(client(driver), "s3cret", nil)
			if !errors.Is(err, apistore.ErrNoNASDriver) {
				t.Errorf("driver %q answered %v, want ErrNoNASDriver — it is driven through "+
					"the controller that adopted it, and a driver built from this row "+
					"would be built from the wrong one", driver, err)
			}
		}
	})
}

// recordingNASDrivers captures what the adapter handed the seam.
//
// A fake, and labelled one: nothing here is evidence about MikroTik. What it is
// evidence about is that the row's own values reach the driver rather than defaults —
// the CoA port especially, which `docs/rules/nas-drivers.md` is explicit is
// per-NAS and not a constant, because Cisco frequently uses 1700.
type recordingNASDrivers struct {
	mu       sync.Mutex
	client   store.RadiusClient
	secret   string
	sessions nas.SessionSource
	built    int
}

func (r *recordingNASDrivers) ForNAS(c store.RadiusClient, secret string, sessions nas.SessionSource) (nas.NASDriver, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.client, r.secret, r.sessions, r.built = c, secret, sessions, r.built+1
	return nas.NewStrictMockDriver(nas.VendorMikroTik), nil
}

// TestTheAdapterHandsThePerNASSeamTheRowsOwnValues.
//
// The three things a CoA driver cannot be built without, and each of them has a wrong
// default that looks like it works: the shared secret (an empty one produces packets
// the NAS silently drops, which reads as *unreachable*), the CoA port (3799 is common
// and Cisco frequently uses 1700), and the session source (without which the driver
// cannot confirm presence at all).
func TestTheAdapterHandsThePerNASSeamTheRowsOwnValues(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	st := openStore(t)
	rec := &recordingNASDrivers{}
	adapter, err := apistore.New(apistore.Config{Store: st, Drivers: fakeDrivers{}, NASDrivers: rec})
	if err != nil {
		t.Fatalf("building the adapter: %v", err)
	}

	site := uniq("site")
	if err := st.Sites().Create(ctx, &store.Site{ID: site, Name: "Router venue", Environment: "lab"}); err != nil {
		t.Fatalf("creating the site: %v", err)
	}
	secret := &store.SharedSecret{ID: uniq("sec"), SiteID: site, Label: "Router"}
	plaintext := randomString()
	if err := st.SharedSecrets().Create(ctx, secret, []byte(plaintext)); err != nil {
		t.Fatalf("creating the shared secret: %v", err)
	}
	n := worldSeq.Add(1)
	client := &store.RadiusClient{
		ID: uniq("rc"), SiteID: site, Name: "Hotspot router",
		Address: netip.MustParsePrefix(fmtPrefix(n)),
		Source:  "manual", Driver: "mikrotik", SharedSecretID: &secret.ID,
		// Cisco's port on a MikroTik row, because the value being carried is the
		// assertion: a constant would pass with the default.
		CoAPort:      ptr(1700),
		Reachability: "unknown", Enabled: true,
	}
	if err := st.RadiusClients().Create(ctx, client); err != nil {
		t.Fatalf("creating the radius client: %v", err)
	}

	// The **device** id, which for a client with no access point is the client's.
	d, got, err := adapter.NASDriverFor(ctx, client.ID)
	if err != nil {
		t.Fatalf("building the driver for %s: %v", client.ID, err)
	}
	if d == nil {
		t.Fatal("no driver was returned and no error")
	}
	if rec.built != 1 {
		t.Fatalf("the seam was called %d times, want 1", rec.built)
	}
	if rec.secret != plaintext {
		t.Errorf("the seam was handed %q, want the unsealed secret — a driver holding "+
			"ciphertext or an empty string signs packets the NAS silently drops", rec.secret)
	}
	if rec.client.CoAPort == nil || *rec.client.CoAPort != 1700 {
		t.Errorf("the seam was handed CoA port %v, want 1700 from the row; the port is "+
			"per-NAS and not a constant", rec.client.CoAPort)
	}
	if rec.sessions == nil {
		t.Error("the seam was handed no session source, so a CoA driver built from it " +
			"cannot confirm any device is present and every grant behind it is refused")
	}
	if got.ID != client.ID {
		t.Errorf("the returned row is %q, want the clients.conf row %q — rotating a secret "+
			"acts on the client, not on the radio", got.ID, client.ID)
	}
}

// TestADeviceWithNoSharedSecretIsRefusedBeforeADriverExists.
//
// Not after. A driver holding an empty secret signs a CoA request the NAS drops
// without answering, which the driver reports as a timeout — so an operator chasing
// *this device is unreachable* would be looking at the network for a field they can
// fill in.
func TestADeviceWithNoSharedSecretIsRefusedBeforeADriverExists(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	st := openStore(t)
	rec := &recordingNASDrivers{}
	adapter, err := apistore.New(apistore.Config{Store: st, Drivers: fakeDrivers{}, NASDrivers: rec})
	if err != nil {
		t.Fatalf("building the adapter: %v", err)
	}

	site := uniq("site")
	if err := st.Sites().Create(ctx, &store.Site{ID: site, Name: "Router venue", Environment: "lab"}); err != nil {
		t.Fatalf("creating the site: %v", err)
	}
	n := worldSeq.Add(1)
	client := &store.RadiusClient{
		ID: uniq("rc"), SiteID: site, Name: "Secretless router",
		Address: netip.MustParsePrefix(fmtPrefix(n)),
		Source:  "manual", Driver: "mikrotik", Reachability: "unknown", Enabled: true,
	}
	if err := st.RadiusClients().Create(ctx, client); err != nil {
		t.Fatalf("creating the radius client: %v", err)
	}

	if _, _, err := adapter.NASDriverFor(ctx, client.ID); !errors.Is(err, apistore.ErrNoNASDriver) {
		t.Fatalf("a device with no shared secret answered %v, want ErrNoNASDriver", err)
	}
	if rec.built != 0 {
		t.Error("the seam was called for a device with no secret; the driver would hold an " +
			"empty one and report every failure as a timeout")
	}
}

// TestAnUnsetCoAVendorNeverReachesADriver is the lock against the failure this
// whole column exists to prevent, and it is written as an anti-guess rather than as
// a happy path.
//
// **Every other refusal in this package is visible.** A bad address is a 422 with
// the field named. An unreachable controller is a probe that says so. A guessed CoA
// vendor is none of that: the packet is well-formed, the shared secret verifies, the
// NAS answers ACK, and the session it was asked to end is still online — through the
// one channel `docs/rules/nas-drivers.md` says a typed `Error-Cause` cannot see,
// and the one `docs/rules/testing.md` says a lab cannot reproduce, because
// *"ACKing a CoA is trivial; faithfully rejecting one is not."*
//
// So this asserts the negative directly: for every value that is not a member of the
// contract's `CoaVendor`, **no driver comes back at all**. It can be made to fail:
// defaulting the empty case to `CiscoCoAVendor{}` in `coaVendorFor` turns it red
// with *"an unset CoA vendor produced a cisco driver"*.
//
// The positive is in `TestVendorDriversBuildWhatTheyCanAndRefuseTheRest` above, and
// it has to be, or "nothing builds" would pass for a seam that builds nothing.
func TestAnUnsetCoAVendorNeverReachesADriver(t *testing.T) {
	base := store.RadiusClient{
		ID: "rc-guess", SiteID: "site-1", Name: "Unlabelled CoA device", Driver: "coa",
		Address: netip.MustParsePrefix("10.91.0.11/32"),
	}

	// Nil, empty, whitespace, the enum's own name, and a plausible-looking
	// neighbour. Each is a value somebody could end up with: a row that predates
	// the column, a blank field, a copy of the driver name, another vendor's.
	unset := []*string{nil, ptr(""), ptr(" "), ptr("coa"), ptr("CISCO"), ptr("mikrotik")}
	for _, v := range unset {
		row := base
		row.CoAVendor = v
		d, err := apistore.VendorDrivers{}.ForNAS(row, "s3cret", nil)
		if err == nil {
			t.Fatalf("coa_vendor %s produced a %s driver; nothing recorded which vendor this "+
				"device is, and the wrong one is answered with an acknowledgement and no effect",
				quoteVendor(v), d.Vendor())
		}
		if d != nil {
			t.Fatalf("coa_vendor %s was refused and still produced a %T", quoteVendor(v), d)
		}
		if !errors.Is(err, apistore.ErrCoAVendorUnknown) {
			t.Errorf("coa_vendor %s was refused as %v, want ErrCoAVendorUnknown — the refusal "+
				"has to be the one the API can recognise and explain", quoteVendor(v), err)
		}
	}
}

func quoteVendor(v *string) string {
	if v == nil {
		return "NULL"
	}
	return strconv.Quote(*v)
}

// fmtPrefix gives each test in this file its own /32 out of the harness's per-world
// space: `radius_clients` is UNIQUE (tenant_id, address) and the container is shared.
func fmtPrefix(n int64) string {
	return fmt.Sprintf("10.%d.%d.11/32", 150+n/250, n%250)
}
