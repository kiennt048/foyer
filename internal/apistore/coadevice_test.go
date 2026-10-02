package apistore_test

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/store"
)

// The two defects this file guards against, and why they are one file.
//
// **The vendor** — `NasDriverKind` has one member, `coa`, for Cisco and Aruba, so
// the row has to record which. **The seam** — the per-NAS driver seam needs a
// caller, or every driver-gated path resolves a controller first and answers *there
// is none*.
//
// Separately each is survivable; together they mean a Cisco device could be
// registered, listed, given a shared secret and shown a green tick, and **nothing in
// the product could ever send it a packet**. The two are asserted together because
// the fix is only observable together: a column with no reader is inert, and a reader
// with no column refuses everything.
//
// Everything about **device behaviour stays UNVERIFIED.** No Cisco or Aruba hardware
// has been near this project, `internal/nas` marks every capability of both
// UNVERIFIED, and nothing here changes that. What is asserted is that the correctly
// labelled driver is *reachable* — which vendor gets built, and that no vendor gets
// built when nobody said.

// TestACiscoDeviceRegisteredThroughTheAPIReachesACiscoDriver drives the whole seam
// through the front door: HTTP, CSRF, PostgreSQL, the repository, the column, the
// driver.
//
// Through the API rather than by writing a row, because the row is not the hard
// part. `radius_clients.coa_vendor` with no writer would be a third inert switch
// beside `nas.require_message_authenticator` and `nas.limit_proxy_state`
// (`docs/rules/security.md` §3). The writer is `createNas`, so `createNas` is what
// this calls.
func TestACiscoDeviceRegisteredThroughTheAPIReachesACiscoDriver(t *testing.T) {
	w := newWorld(t)
	w.signIn()
	ctx := context.Background()

	for _, c := range []struct {
		vendor gen.CoaVendor
		want   nas.Vendor
	}{
		{gen.Cisco, nas.VendorCisco},
		{gen.Aruba, nas.VendorAruba},
	} {
		t.Run(string(c.vendor), func(t *testing.T) {
			raw := w.do(http.MethodPost, "/nas", map[string]any{
				"siteId": w.siteA, "name": "Campus " + string(c.vendor), "driver": "coa",
				"ipAddress": w.nextAddr(), "coaVendor": string(c.vendor),
			}, http.StatusCreated)
			var created struct {
				Nas gen.Nas `json:"nas"`
			}
			mustJSON(t, raw, &created)

			// The read model carries it back, or the console cannot show an
			// operator which vendor the device they registered is.
			var back gen.Nas
			w.getJSON("/nas/"+created.Nas.Id, &back)
			if back.CoaVendor == nil || *back.CoaVendor != c.vendor {
				t.Fatalf("getNas answers coaVendor = %v, want %q", back.CoaVendor, c.vendor)
			}

			// And the driver. This is the assertion the whole change is for: the
			// two vendors are the same Go type and differ only in the value inside
			// them, so "a driver was built" proves nothing on its own.
			d, row, err := w.adapter.NASDriverFor(ctx, created.Nas.Id)
			if err != nil {
				t.Fatalf("building the driver for a registered %s device: %v", c.vendor, err)
			}
			if _, ok := d.(*nas.CoADriver); !ok {
				t.Fatalf("built a %T, want *nas.CoADriver", d)
			}
			if got := d.Vendor(); got != c.want {
				t.Fatalf("a device registered as %q reaches a %s driver, want %s — the two "+
					"identify a session differently and the wrong one is answered with an "+
					"acknowledgement and no effect", c.vendor, got, c.want)
			}
			if row.CoAVendor == nil || *row.CoAVendor != string(c.vendor) {
				t.Errorf("the stored row records coa_vendor = %v; the driver was built from "+
					"something other than the column", row.CoAVendor)
			}

			// Nothing was measured about the device, and the driver says so. A
			// capability promoted on the strength of this wiring would be a
			// control on a screen for hardware nobody has seen.
			if u := nas.UnverifiedCapabilities(d); len(u) == 0 {
				t.Errorf("a %s driver reports every capability as verified; no device of this "+
					"vendor has been near this project", c.want)
			}
		})
	}
}

// TestRegisteringCoAWithNoVendorIsRefusedByName is the first of the two gates.
//
// It refuses at the point the row would be created, field-level, so the console can
// mark the field rather than the form. `required` and not a new fault kind: the
// operator's job is to fill it in, which is what `required` already means, and the
// console's copy for it is already written.
func TestRegisteringCoAWithNoVendorIsRefusedByName(t *testing.T) {
	w := newWorld(t)
	w.signIn()

	raw := w.do(http.MethodPost, "/nas", map[string]any{
		"siteId": w.siteA, "name": "Unlabelled CoA device", "driver": "coa",
		"ipAddress": w.nextAddr(),
	}, http.StatusUnprocessableEntity)

	var problem struct {
		Type   string         `json:"type"`
		Errors map[string]any `json:"errors"`
		Params struct {
			Fields map[string]string `json:"fields"`
		} `json:"params"`
	}
	mustJSON(t, raw, &problem)
	if problem.Params.Fields["coaVendor"] != string(gen.FieldFaultRequired) {
		t.Fatalf("the refusal does not mark coaVendor as required: %s", truncate(raw))
	}
	// The two key sets are the same set — a fault with no `errors` entry is a field
	// the client cannot describe, and the contract says so of every refusal.
	if _, ok := problem.Errors["coaVendor"]; !ok {
		t.Errorf("the refusal carries a fault for coaVendor and no message for it: %s", truncate(raw))
	}

	// And the other drivers are unaffected: the field is required for one of the
	// five, and a gate that refused all of them would be a different defect.
	w.do(http.MethodPost, "/nas", map[string]any{
		"siteId": w.siteA, "name": "Hotspot router", "driver": "mikrotik",
		"ipAddress": w.nextAddr(),
	}, http.StatusCreated)
}

// TestACoARowWithNoVendorIsRefusedADriverByName is the second gate, and it is the
// one that cannot be replaced by the first.
//
// `createNas` stops a *new* row being made without a vendor. It cannot reach a row
// that already exists — one written before migration 00015, or by anything that is
// not the registration path — and `radius_clients.coa_vendor` is nullable precisely
// so that *nobody has said* stays representable rather than being invented by a
// migration with nothing to decide on.
//
// So the row is written straight through the repository here, which is the shape the
// API cannot produce and the database still can.
func TestACoARowWithNoVendorIsRefusedADriverByName(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	secret := &store.SharedSecret{ID: uniq("sec"), SiteID: w.siteA, Label: "Campus edge"}
	if err := w.st.SharedSecrets().Create(ctx, secret, []byte(randomString())); err != nil {
		t.Fatalf("creating the shared secret: %v", err)
	}
	client := &store.RadiusClient{
		ID: uniq("rc"), SiteID: w.siteA, Name: "Legacy CoA device",
		Address: mustPrefix(w.nextAddr()), Source: "manual", Driver: "coa",
		SharedSecretID: ptr(secret.ID), Reachability: "unknown", Enabled: true,
	}
	if err := w.st.RadiusClients().Create(ctx, client); err != nil {
		t.Fatalf("creating a coa client with no vendor: %v", err)
	}

	d, _, err := w.adapter.NASDriverFor(ctx, client.ID)
	if err == nil {
		t.Fatalf("a coa row recording no vendor produced a %s driver; the wrong dialect "+
			"identifies no session and a careless NAS answers that with an ACK", d.Vendor())
	}
	if !errors.Is(err, apistore.ErrCoAVendorUnknown) {
		t.Fatalf("the refusal is %v, want ErrCoAVendorUnknown", err)
	}
	if !strings.Contains(err.Error(), "Legacy CoA device") {
		t.Errorf("the refusal does not name the device an operator has to fix: %q", err)
	}

	// And it reaches an operator rather than only a log. `probeNas` is the control
	// they press, so it is what has to carry the sentence.
	w.signIn()
	raw := w.do(http.MethodPost, "/nas/"+client.ID+"/probe", nil, http.StatusOK)
	var probe gen.NasProbe
	mustJSON(t, raw, &probe)

	var driverCheck *string
	for _, c := range probe.Checks {
		if c.Name == "driver" {
			if c.Result != gen.NasProbeChecksResultFail {
				t.Fatalf("the driver check reports %q for a device Foyer cannot build one for", c.Result)
			}
			driverCheck = c.Detail
		}
	}
	if driverCheck == nil {
		t.Fatalf("probeNas reports no driver check at all for a device with no controller: %s", truncate(raw))
	}
	for _, want := range []string{"Cisco", "Aruba"} {
		if !strings.Contains(*driverCheck, want) {
			t.Errorf("the probe's explanation does not name %s: %q", want, *driverCheck)
		}
	}
	// The sentence an operator reads must not be the Go error: it carries a package
	// name, and `internal/api`'s rule is that an API error never shows one.
	if strings.Contains(*driverCheck, "apistore") {
		t.Errorf("the probe leaked an internal package name to the console: %q", *driverCheck)
	}
}

// TestAControllerDeviceNeverReachesThePerNASSeam is the negative the seam needs, and it
// is worth nothing without the positive beside it.
//
// The failure mode of wiring the per-NAS seam into `probeNas`, `rotateNasSecret` and
// the session-end path is **quietly rerouting UniFi through a path it does not
// want**: a UniFi access point is driven through the controller that adopted it, and
// a CoA driver built from its `radius_clients` row would aim RFC 5176 at a radio with
// no CoA listener and a secret no operator ever typed into it.
//
// One recording fake, one world, both directions. A `built == 0` on its own is
// equally satisfied by a seam nothing calls, which is exactly the kind of vacuous
// check `docs/rules/testing.md` describes: an input the harness cannot be wrong
// about.
func TestAControllerDeviceNeverReachesThePerNASSeam(t *testing.T) {
	rec := &recordingNASDrivers{}
	w := newWorldWith(t, func(cfg *apistore.Config) { cfg.NASDrivers = rec })
	w.signIn()

	// ── the device that has a controller ─────────────────────────────────────
	//
	// Rotation before the probe, and the order is not arbitrary: `probeNas` writes
	// back what it measured, and the fake controller does not report this world's
	// access point, so a probe first leaves the device `unreachable` and rotation
	// is then refused for a reason that has nothing to do with this test.
	w.do(http.MethodPost, "/nas/"+w.deviceOK+"/secret", nil, http.StatusOK)
	w.do(http.MethodPost, "/nas/"+w.deviceOK+"/probe", nil, http.StatusOK)
	// The session end, through the operation that reaches it. `sessionOnline` is
	// on the device with the controller.
	w.do(http.MethodPost, "/vouchers/"+w.voucherInUse+"/revoke", nil, http.StatusOK)

	if rec.built != 0 {
		t.Fatalf("a device with a controller reached the per-NAS seam %d time(s); it is driven "+
			"through the controller that adopted it, and a driver built from its clients.conf "+
			"row would be built from the wrong one", rec.built)
	}

	// ── and the device that does not ─────────────────────────────────────────
	//
	// Same fake, same world, same operations. Without this the assertion above is
	// satisfied by a seam nothing calls.
	raw := w.do(http.MethodPost, "/nas", map[string]any{
		"siteId": w.siteA, "name": "Hotspot router", "driver": "mikrotik",
		"ipAddress": w.nextAddr(),
	}, http.StatusCreated)
	var created struct {
		Nas gen.Nas `json:"nas"`
	}
	mustJSON(t, raw, &created)

	w.do(http.MethodPost, "/nas/"+created.Nas.Id+"/probe", nil, http.StatusOK)
	if rec.built == 0 {
		t.Fatal("a device with no controller reached the per-NAS seam zero times, so the " +
			"assertion above is about a seam nothing calls rather than about UniFi")
	}
	if rec.client.ID != created.Nas.Id {
		t.Errorf("the seam was handed row %q for device %q", rec.client.ID, created.Nas.Id)
	}
}

// mustPrefix turns one of the harness's device addresses into the single-host prefix
// `radius_clients.address` holds.
func mustPrefix(addr string) netip.Prefix {
	return netip.MustParsePrefix(addr + "/32")
}
