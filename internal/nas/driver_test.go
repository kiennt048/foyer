package nas

import (
	"context"
	"testing"
)

func TestMockDriver_Lifecycle(t *testing.T) {
	driver := NewMockDriver(VendorUniFi)

	// 1. Verify Capabilities
	caps := driver.Capabilities()
	if !caps.CanAuthorizeGuest || !caps.CanDeauthGuest {
		t.Errorf("expected mock driver to support authorize and deauth")
	}

	// 2. VerifyPresence
	present, err := driver.VerifyPresence(context.Background(), "00:11:22:33:44:55", "aa:bb:cc:dd:ee:ff", "site-1")
	if err != nil {
		t.Fatalf("VerifyPresence failed: %v", err)
	}
	if !present {
		t.Errorf("expected default presence to be true")
	}

	// 3. AuthorizeGuest
	err = driver.AuthorizeGuest(context.Background(), AuthorizeRequest{
		ClientMAC:        "00:11:22:33:44:55",
		APMAC:            "aa:bb:cc:dd:ee:ff",
		TimeLimitMinutes: 60,
		DataUsageLimitMB: 500,
	})
	if err != nil {
		t.Fatalf("AuthorizeGuest failed: %v", err)
	}

	if _, exists := driver.Authorized["00:11:22:33:44:55"]; !exists {
		t.Errorf("expected client to be authorized in mock")
	}

	// 4. DeauthorizeGuest
	err = driver.DeauthorizeGuest(context.Background(), DeauthorizeRequest{
		ClientMAC: "00:11:22:33:44:55",
		Reason:    "Quota exceeded",
	})
	if err != nil {
		t.Fatalf("DeauthorizeGuest failed: %v", err)
	}

	if _, exists := driver.Authorized["00:11:22:33:44:55"]; exists {
		t.Errorf("expected client to be removed from authorized set")
	}
}

// TestMockDriver_DefaultPresenceIsAChoice pins the difference between the two
// constructors, because it is the difference between a test that can catch a
// missing presence check and one that cannot.
func TestMockDriver_DefaultPresenceIsAChoice(t *testing.T) {
	permissive := NewMockDriver(VendorUniFi)
	strict := NewStrictMockDriver(VendorUniFi)
	const unknown = "de:ad:be:ef:00:02"

	if ok, _ := permissive.VerifyPresence(context.Background(), unknown, "", "site-1"); !ok {
		t.Errorf("NewMockDriver changed behaviour; callers depend on the permissive default")
	}
	if ok, _ := strict.VerifyPresence(context.Background(), unknown, "", "site-1"); ok {
		t.Errorf("NewStrictMockDriver confirmed a MAC nobody told it about, which is the assumption " +
			"VerifyPresence exists to refuse")
	}

	strict.Presences[unknown] = true
	if ok, _ := strict.VerifyPresence(context.Background(), unknown, "", "site-1"); !ok {
		t.Errorf("an explicitly present MAC was refused")
	}
}

// unevidencedDriver implements NASDriver and not EvidencedDriver.
//
// It is declared here rather than reusing MockDriver because MockDriver now
// *does* carry evidence — derived from the shipped driver for its vendor — and a
// test whose subject is "a driver that cannot say how it knows" needs a driver
// that cannot say, not one that happens not to have been asked.
type unevidencedDriver struct{ NASDriver }

// TestUnverifiedCapabilities_NoEvidenceMeansNoClaim: a driver that cannot say
// how it knows something is reported as knowing nothing, rather than being
// given the benefit of the doubt.
func TestUnverifiedCapabilities_NoEvidenceMeansNoClaim(t *testing.T) {
	if got := UnverifiedCapabilities(unevidencedDriver{}); got != nil {
		t.Errorf("UnverifiedCapabilities on a driver with no evidence = %v, want nil", got)
	}
}

// TestMockDriver_ClaimsNoMoreThanTheShippedDriver locks the mock to the shipped
// driver's capabilities.
//
// The mock's capabilities were a literal, and the literal was the most capable
// driver that could exist: WalledGardenFQDN where the only shipped driver
// reports WalledGardenNone, and RotateSecret where it reports false. Every Go
// test reaching a capability gate through this mock was reaching the branch no
// deployment takes, including a 500 the shipped driver cannot produce.
//
// Restoring either value turns this red, which is the point: the assertion is
// equality with the shipped driver, so it cannot drift from unifi.go and it
// cannot be satisfied by a second copy of the same fields.
func TestMockDriver_ClaimsNoMoreThanTheShippedDriver(t *testing.T) {
	shipped := NewUniFiDriver(UniFiConfig{BaseURL: "https://unifi.invalid"}).Capabilities()

	for _, ctor := range []struct {
		name string
		make func(Vendor) *MockDriver
	}{
		{"NewMockDriver", NewMockDriver},
		{"NewStrictMockDriver", NewStrictMockDriver},
	} {
		t.Run(ctor.name, func(t *testing.T) {
			if got := ctor.make(VendorUniFi).Capabilities(); got != shipped {
				t.Errorf("capabilities = %+v, shipped UniFi driver = %+v — a fake more capable than "+
					"the real thing does not fail, it invents", got, shipped)
			}
		})
	}

	// The two values a hand-written fake gets wrong most easily, named, so a
	// failure says which one moved rather than printing two structs and leaving
	// the reader to diff them.
	caps := NewMockDriver(VendorUniFi).Capabilities()
	if caps.WalledGarden != WalledGardenNone {
		t.Errorf("WalledGarden = %q, want %q — the shipped UniFi driver cannot maintain a garden at all",
			caps.WalledGarden, WalledGardenNone)
	}
	if caps.RotateSecret {
		t.Error("RotateSecret is true; no vendor in the design can push a shared secret to a device, " +
			"and a fake that claims it sends a harness into PushSecret's error path")
	}
}

// TestMockDriver_VendorWithNoShippedDriverClaimsNothing.
//
// A mock standing in for a vendor Foyer has no driver for has nothing to derive
// a claim from, so it claims the floor. Being wrong in this direction is the
// cheap direction: a test that assumes a capability is absent fails loudly.
//
// **The vendor list is derived.** A hardcoded list of vendors without a driver
// goes stale the moment a driver is added, and would have to be edited to stay
// true; a derived one is true by construction, and the same run gets the
// symmetric half for free.
func TestMockDriver_VendorWithNoShippedDriverClaimsNothing(t *testing.T) {
	shipped := make(map[Vendor]bool)
	for _, v := range ShippedVendors() {
		shipped[v] = true
	}
	if len(shipped) == 0 {
		t.Fatal("no shipped drivers registered at all; this test would be vacuous")
	}

	all := []Vendor{VendorUniFi, VendorOmada, VendorAruba, VendorCisco, VendorRuckus, VendorMikroTik, VendorManual}
	var withDriver, without int
	for _, v := range all {
		caps := NewMockDriver(v).Capabilities()
		if shipped[v] {
			// The symmetric half: where a driver exists the fake must claim
			// exactly what it claims, or it is the same defect one vendor along.
			withDriver++
			if want := ShippedCapabilities(v); caps != want {
				t.Errorf("%s: mock capabilities = %+v, shipped driver = %+v", v, caps, want)
			}
			continue
		}
		without++
		if caps != (Capabilities{WalledGarden: WalledGardenNone}) {
			t.Errorf("%s: capabilities = %+v, want the floor — Foyer has no driver for this vendor", v, caps)
		}
	}
	// Both halves must be non-empty, or one of them is a loop over nothing that
	// reports success. Foyer ships drivers for some vendors and not others, and
	// the day that stops being true this test is the thing to rewrite.
	if withDriver == 0 || without == 0 {
		t.Fatalf("%d vendors with a driver and %d without; a half with no rows proves nothing",
			withDriver, without)
	}
}

// TestMockDriver_WithCapabilities_IsTheOnlyWayUp.
//
// The opt-in exists so that a test needing a capability the shipped driver does
// not have writes that down. It raises the claim and deliberately leaves the
// evidence alone: raising a value in a test does not measure it.
func TestMockDriver_WithCapabilities_IsTheOnlyWayUp(t *testing.T) {
	d := NewMockDriver(VendorUniFi).WithCapabilities(func(c *Capabilities) {
		c.RotateSecret = true
	})
	if !d.Capabilities().RotateSecret {
		t.Error("WithCapabilities did not raise RotateSecret")
	}
	if fact := d.CapabilityEvidence()[CapRotateSecret]; fact.Evidence != EvidenceUnverified {
		t.Errorf("evidence for rotateSecret = %q after an opt-in, want %q — an opt-in is an assumption, "+
			"not a measurement", fact.Evidence, EvidenceUnverified)
	}
}

// TestMockDriver_ProbeNamesTheUnmeasuredCapabilities.
//
// The shipped UniFi probe reports three capabilities it cannot back with a
// measurement, and ProbeResult.Unverified exists so a registration screen can
// refuse to present one as a fact. A fake whose probe named none was a
// controller with no caveats, which is not a controller anybody has.
func TestMockDriver_ProbeNamesTheUnmeasuredCapabilities(t *testing.T) {
	shipped := UnverifiedCapabilities(NewUniFiDriver(UniFiConfig{BaseURL: "https://unifi.invalid"}))
	if len(shipped) == 0 {
		t.Fatal("the shipped UniFi driver reports nothing unverified; this test has lost its subject")
	}

	res, err := NewMockDriver(VendorUniFi).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(res.Unverified) != len(shipped) {
		t.Errorf("probe named %v as unverified, shipped driver names %v", res.Unverified, shipped)
	}
	for i, name := range shipped {
		if i < len(res.Unverified) && res.Unverified[i] != name {
			t.Errorf("unverified[%d] = %q, want %q", i, res.Unverified[i], name)
		}
	}
}

// TestMockDriver_ProbeCarriesTheShippedDriversUnconditionalWarnings.
//
// The same rule as the test above, applied to the probe warnings: whatever
// the shipped driver for a vendor attaches to every probe, the fake attaches
// too, or every test that reaches `warnings` through it is exercising a
// controller that has no caveats. Compared against `ShippedProbeWarnings` rather
// than against a literal, which is the kind of copy that misleads every test
// downstream of it.
//
// `ShippedProbeWarnings(VendorUniFi)` is empty *because the UniFi driver confirms
// the access point*, so the vendor with an unconditional caveat is the CoA
// family. The list is read off whichever vendor has one rather than named here,
// so this keeps its subject the next time one of them changes.
func TestMockDriver_ProbeCarriesTheShippedDriversUnconditionalWarnings(t *testing.T) {
	var vendor Vendor
	var shipped []ProbeWarning
	for _, v := range ShippedVendors() {
		if w := ShippedProbeWarnings(v); len(w) > 0 {
			vendor, shipped = v, w
			break
		}
	}
	if len(shipped) == 0 {
		t.Fatal("no shipped driver reports an unconditional warning; this test has lost its subject")
	}

	for _, tc := range []struct {
		name string
		d    *MockDriver
	}{
		{"a controller that answers", NewMockDriver(vendor)},
		// The failure paths too. A controller nobody can reach has not answered
		// the access-point question; it has failed to be asked it.
		{"one nobody can reach", NewMockDriver(vendor).WithControllerUnreachable(nil)},
		{"one that refuses the probe", NewMockDriver(vendor).WithFaults(func(f *MockFaults) {
			f.Probe = MockAPIRefusal(401, "api key rejected")
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := tc.d.Probe(context.Background())
			if res == nil {
				t.Fatal("Probe returned no result at all; a registration screen has nothing to render")
			}
			for _, want := range shipped {
				got, ok := findWarning(res.Warnings, want.Kind)
				if !ok {
					t.Fatalf("probe warnings %v carry no %q, which the shipped %s driver reports on "+
						"every probe", warningKinds(res.Warnings), want.Kind, vendor)
				}
				for k, v := range want.Params {
					if got.Params[k] != v {
						t.Errorf("%s params[%q] = %v, want %v", want.Kind, k, got.Params[k], v)
					}
				}
			}
		})
	}
}

// TestMockDriver_AccessPointNoticeFollowsThisFakesOwnWorld.
//
// The half that could not exist while the notice was a constant, and the reason
// the constant had to go.
//
// `NewMockDriver(VendorUniFi)` stands in for a UniFi controller. The shipped
// UniFi driver *confirms* the access point, so copying its (now empty)
// unconditional list into the fake would have said **this mock controller checks
// the access point** — while the mock's own `VerifyPresenceDetail` went on
// answering `APConfirmed: false` for every client, because a mock with no client
// records has nothing to compare. The probe and the presence answer would have
// described two different controllers.
//
// So the notice is derived from the same field `APConfirmed` is derived from,
// and this drives both directions: a fake with an access point in it must not
// warn, and one without must. Either half alone is green against a constant.
func TestMockDriver_AccessPointNoticeFollowsThisFakesOwnWorld(t *testing.T) {
	const guest = "aa:bb:cc:dd:ee:ff"
	const ap = "94:2a:6f:d0:30:57"

	for _, tc := range []struct {
		name string
		d    *MockDriver
		want bool
	}{
		{
			name: "a controller that places no client under any access point",
			d:    NewMockDriver(VendorUniFi),
			want: true,
		},
		{
			name: "one that does",
			d: NewMockDriver(VendorUniFi).
				WithClient(guest, MockClient{ID: "c-1", APMAC: ap}),
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.d.Probe(context.Background())
			if err != nil || res == nil {
				t.Fatalf("Probe: res = %v, err = %v", res, err)
			}
			_, warned := findWarning(res.Warnings, WarnAccessPointUnconfirmed)
			if warned != tc.want {
				t.Errorf("%q present = %v, want %v; warnings were %v",
					WarnAccessPointUnconfirmed, warned, tc.want, warningKinds(res.Warnings))
			}

			// And the presence answer must agree with the probe about the same
			// controller. Two surfaces, one fact — this is the assertion that
			// goes red if either is ever wired to a constant again.
			p, err := tc.d.VerifyPresenceDetail(context.Background(), guest, ap, "")
			if err != nil {
				t.Fatalf("VerifyPresenceDetail: %v", err)
			}
			if p.APConfirmed == warned {
				t.Errorf("the probe says the access point is unconfirmable (%v) and a presence "+
					"answer on the same fake says APConfirmed = %v", warned, p.APConfirmed)
			}
		})
	}
}

// TestNoWarningHidesAFigureInsideItsSentence.
//
// The rule, stated as a property rather than per-kind: a warning that takes
// a parameter must carry it in Params. Detail is for a human reading a Go value
// — `foyerctl` output, a log line — and the admin API never sends it, so a
// figure that exists only inside that string is a figure the console can only
// get back by parsing English. That is the hand-maintained copy with a regular
// expression on top, and it is what the wire format refuses to be.
func TestNoWarningHidesAFigureInsideItsSentence(t *testing.T) {
	for _, w := range []ProbeWarning{
		VersionBelowFloorWarning("8.6.9", "9.0.108"),
		AccessPointUnconfirmedWarning("the controller could not answer the device list"),
	} {
		if w.Detail == "" {
			t.Errorf("%s carries no sentence at all; foyerctl and the support bundle read this one",
				w.Kind)
		}
		if len(w.Params) == 0 {
			t.Errorf("%s carries a sentence and no params — every figure in it is then reachable "+
				"only by parsing the sentence", w.Kind)
		}
	}

	// And the derived one refuses to exist over nothing.
	if _, ok := CapabilitiesUnverifiedWarning(nil); ok {
		t.Error("an empty unverified list produced a warning; a caveat over a controller with no " +
			"unmeasured capability is a caveat nobody has")
	}
	w, ok := CapabilitiesUnverifiedWarning([]string{CapLiveReauthorize, CapRotateSecret})
	if !ok {
		t.Fatal("two unmeasured capabilities produced no warning")
	}
	if w.Params["count"] != 2 {
		t.Errorf("count = %v, want 2 — the console interpolates this figure and cannot compute it",
			w.Params["count"])
	}
}

// TestMockDriver_WillNotAuthorizeAMACItDoesNotHold.
//
// UniFi's AuthorizeGuest resolves the client on the controller first and returns
// `no client %s in site %s` when it cannot, so a device the controller has never
// seen cannot be authorized. The mock authorized anything, which made it more
// agreeable than the driver it stands in for on the one call that puts a guest
// on the network.
func TestMockDriver_WillNotAuthorizeAMACItDoesNotHold(t *testing.T) {
	strict := NewStrictMockDriver(VendorUniFi)
	const mac = "de:ad:be:ef:00:03"

	if err := strict.AuthorizeGuest(context.Background(), AuthorizeRequest{ClientMAC: mac, SiteID: "site-1"}); err == nil {
		t.Error("a strict mock authorized a MAC nobody told it about; the shipped driver cannot")
	}
	if _, ok := strict.Authorized[mac]; ok {
		t.Error("a refused authorization was recorded anyway")
	}

	strict.Presences[mac] = true
	if err := strict.AuthorizeGuest(context.Background(), AuthorizeRequest{ClientMAC: mac, SiteID: "site-1"}); err != nil {
		t.Errorf("an associated MAC was refused: %v", err)
	}
}
