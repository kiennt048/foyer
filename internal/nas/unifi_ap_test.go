package nas

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The access-point clause of security.md §4, on the UniFi path.
//
// ── what these lock, and what made them red ──────────────────────────────────
//
// The clause once looked unsatisfiable: `VerifyPresenceDetail` checked the MAC,
// the site and still-unauthorized, and accepted whatever access point the
// unsigned redirect claimed. **It was measured otherwise** — the client object
// carries `uplinkDeviceId` — and the driver satisfies it.
//
// The headline case is `a client on a different access point than the redirect
// claims`. Against the driver as it stood it answered `verified`, OK=true: the
// AP argument reached one `if` that could only ever refuse, and never a
// comparison. It is red on the unfixed driver and green on this one, which is
// the only thing that says these tests are about the product rather than about
// themselves.
//
// The fixture world has **two** access points on purpose. With one, "the
// claimed AP is not the one the client is under" and "the claimed AP is not a
// device at all" are the same bytes, and a driver that resolved nothing would
// pass a mismatch test — an input the harness cannot be wrong about
// (`docs/rules/testing.md`, Principles).

const (
	// The lobby AP, and the id the with-uplink client fixture points at. The MAC
	// is the one in Ubiquiti's own worked example of a portal redirect.
	testAPLobbyMAC = "94:2a:6f:d0:30:57"
	// The second access point in the same site, in the shape the Integration API
	// answers for one.
	testAPSecondMAC = "00:00:5e:00:53:01"
)

func TestUniFiVerifyPresence_AccessPointClause(t *testing.T) {
	cases := []struct {
		name        string
		clientsFile string
		devicesFile string
		apMAC       string
		requireAP   bool
		wantOK      bool
		wantOutcome PresenceOutcome
		wantConfirm bool
		wantErr     bool
	}{
		{
			// The case that matters most. An attacker-writable query parameter naming
			// an access point the client is demonstrably not under must not
			// produce a grant, whatever RequireAPMatch says: this is not an
			// unchecked claim, it is a checked and contradicted one.
			name:        "a client on a different access point than the redirect claims",
			clientsFile: "unifi-clients-with-uplink.json",
			apMAC:       testAPSecondMAC,
			wantOutcome: PresenceAPMismatch,
		},
		{
			name:        "and it stays a refusal when nobody asked for one",
			clientsFile: "unifi-clients-with-uplink.json",
			apMAC:       strings.ToUpper(testAPSecondMAC),
			requireAP:   false,
			wantOutcome: PresenceAPMismatch,
		},
		{
			name:        "the access point the client is actually under",
			clientsFile: "unifi-clients-with-uplink.json",
			apMAC:       testAPLobbyMAC,
			wantOK:      true,
			wantOutcome: PresenceVerified,
			wantConfirm: true,
		},
		{
			name:        "notation is not identity — a MAC in another shape is the same AP",
			clientsFile: "unifi-clients-with-uplink.json",
			apMAC:       "94-2A-6F-D0-30-57",
			wantOK:      true,
			wantOutcome: PresenceVerified,
			wantConfirm: true,
		},
		{
			// Nothing was claimed, so nothing is confirmed and nothing is
			// refused. APConfirmed says so rather than borrowing the answer from
			// the run before it.
			name:        "no access point claimed is not a mismatch",
			clientsFile: "unifi-clients-with-uplink.json",
			apMAC:       "",
			wantOK:      true,
			wantOutcome: PresenceVerified,
			wantConfirm: false,
		},
		{
			// The residue the mismatch check leaves behind: a controller whose
			// client object carries no uplink at all. Unconfirmable is reported
			// as unconfirmable and passes — the exception rather than the rule.
			name:        "a client the controller places under no device is unconfirmed, not refused",
			clientsFile: "unifi-clients-unauthorized.json",
			apMAC:       testAPLobbyMAC,
			wantOK:      true,
			wantOutcome: PresenceVerified,
			wantConfirm: false,
		},
		{
			name:        "and RequireAPMatch is what turns that residue into a refusal",
			clientsFile: "unifi-clients-unauthorized.json",
			apMAC:       testAPLobbyMAC,
			requireAP:   true,
			wantOutcome: PresenceAPMismatch,
		},
		{
			// An uplink id no device in the site answers to. Not a mismatch:
			// nothing was compared. The claimed AP may be real and this
			// controller may simply not place the client — an access point
			// adopted between the two calls produces exactly this.
			name:        "an uplink id the site has no device for is unconfirmed",
			clientsFile: "unifi-clients-uplink-unknown.json",
			apMAC:       testAPLobbyMAC,
			wantOK:      true,
			wantOutcome: PresenceVerified,
			wantConfirm: false,
		},
		{
			name:        "and RequireAPMatch refuses that too",
			clientsFile: "unifi-clients-uplink-unknown.json",
			apMAC:       testAPLobbyMAC,
			requireAP:   true,
			wantOutcome: PresenceAPMismatch,
		},
		{
			// A site with no devices at all cannot resolve any uplink. It is the
			// same unconfirmable answer and not an error: the call succeeded, it
			// just had nothing in it.
			name:        "a site with no devices resolves nothing and refuses nothing",
			clientsFile: "unifi-clients-with-uplink.json",
			devicesFile: "unifi-devices-empty.json",
			apMAC:       testAPLobbyMAC,
			wantOK:      true,
			wantOutcome: PresenceVerified,
			wantConfirm: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFixtureController(t, fixtureConfig{
				flavour:     FlavourUniFiOS,
				clientsFile: tc.clientsFile,
				devicesFile: tc.devicesFile,
			})
			d := fc.driver(t, func(c *UniFiConfig) { c.RequireAPMatch = tc.requireAP })

			got, err := d.VerifyPresenceDetail(context.Background(), testMAC, tc.apMAC, "default")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got.OK != tc.wantOK {
				t.Errorf("OK = %v, want %v (outcome %q)", got.OK, tc.wantOK, got.Outcome)
			}
			if got.Outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", got.Outcome, tc.wantOutcome)
			}
			if got.APConfirmed != tc.wantConfirm {
				t.Errorf("APConfirmed = %v, want %v — the field reports whether the claim was "+
					"checked, and a constant in either direction is the AP-mismatch bypass",
					got.APConfirmed, tc.wantConfirm)
			}
			if got.APConfirmed && !got.OK {
				t.Error("APConfirmed on an answer that is not a pass; the field means the claim " +
					"was checked and held")
			}
		})
	}
}

// TestUniFiVerifyPresence_NoAPClaimCostsNoCall: the second call is paid per
// grant, and only when there is something to spend it on.
//
// The cost argument is *one extra controller call per grant*. A driver that
// fetched the device list for a redirect carrying no `ap` would be paying it for
// nothing, on the path where the guest is already waiting.
func TestUniFiVerifyPresence_NoAPClaimCostsNoCall(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{
		flavour:     FlavourUniFiOS,
		clientsFile: "unifi-clients-with-uplink.json",
	})
	d := fc.driver(t)

	if _, err := d.VerifyPresenceDetail(context.Background(), testMAC, "", "default"); err != nil {
		t.Fatalf("VerifyPresenceDetail: %v", err)
	}
	if n := fc.countMatching(http.MethodGet, "/devices"); n != 0 {
		t.Errorf("%d device-list calls for a redirect that claimed no access point; want 0", n)
	}
}

// TestUniFiAccessPointMapIsCachedWithABoundedTTL.
//
// Both halves, because either alone is satisfied by the wrong implementation: a
// cache with no bound passes the first, and no cache at all passes the second.
//
// The bound is a named value and the test reads it rather than restating it —
// `docs/rules/database.md`'s rule about not keeping a second copy of a
// constraint applies to a constant just as much as to an index.
func TestUniFiAccessPointMapIsCachedWithABoundedTTL(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{
		flavour:     FlavourUniFiOS,
		clientsFile: "unifi-clients-with-uplink.json",
	})
	d := fc.driver(t)

	clock := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return clock }

	for i := 0; i < 3; i++ {
		p, err := d.VerifyPresenceDetail(context.Background(), testMAC, testAPLobbyMAC, "default")
		if err != nil || !p.APConfirmed {
			t.Fatalf("grant %d: APConfirmed = %v, err = %v", i, p.APConfirmed, err)
		}
	}
	if n := fc.countMatching(http.MethodGet, "/devices"); n != 1 {
		t.Errorf("%d device-list calls for three grants inside the TTL; the steady state is "+
			"one", n)
	}

	// One second past the bound, and the map is re-read. A device map that never
	// expires is one where an access point mounted this morning is unresolvable
	// until the process restarts.
	clock = clock.Add(unifiDeviceMapTTL + time.Second)
	p, err := d.VerifyPresenceDetail(context.Background(), testMAC, testAPLobbyMAC, "default")
	if err != nil || !p.APConfirmed {
		t.Fatalf("after the TTL: APConfirmed = %v, err = %v", p.APConfirmed, err)
	}
	if n := fc.countMatching(http.MethodGet, "/devices"); n != 2 {
		t.Errorf("%d device-list calls after the TTL expired; want 2", n)
	}
}

// TestUniFiAccessPointMapWalksEveryPage.
//
// MEASURED on Network 10.5.67: the device list is paginated and
// `limit` is **silently clamped** — `?limit=201` answers 200 with `"limit":200`
// in the envelope. So a driver that asks for one large page and trusts the
// answer reads a truncated list as a complete one, and every access point past
// the cut becomes unresolvable. An unresolvable AP is a false *unconfirmed*
// today and a false *refusal* under RequireAPMatch, which is a venue where the
// twenty-sixth access point cannot let anybody on.
//
// The fixture clamps at one row per page, so the second device is reachable only
// by walking `offset`.
func TestUniFiAccessPointMapWalksEveryPage(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{
		flavour:       FlavourUniFiOS,
		clientsFile:   "unifi-clients-with-uplink.json",
		devicePageCap: 1,
	})
	d := fc.driver(t)

	// The lobby AP is the *first* row, so a truncating driver still confirms it.
	// The device the client is not under is the second, and a mismatch can only
	// be detected by a driver that read past the first page.
	p, err := d.VerifyPresenceDetail(context.Background(), testMAC, testAPSecondMAC, "default")
	if err != nil {
		t.Fatalf("VerifyPresenceDetail: %v", err)
	}
	if p.Outcome != PresenceAPMismatch {
		t.Errorf("outcome = %q, want %q — the claimed AP is on the second page, and a driver that "+
			"stopped at the first cannot tell a forged access point from an unknown one",
			p.Outcome, PresenceAPMismatch)
	}

	aps, err := d.DiscoverAccessPoints(context.Background())
	if err != nil {
		t.Fatalf("DiscoverAccessPoints: %v", err)
	}
	if len(aps) != 2 {
		t.Errorf("discovery found %d access points behind a one-row page; want 2. A site's twenty-"+
			"sixth device is invisible to a single unpaged call", len(aps))
	}
}

// TestUniFiVerifyPresence_DeviceListFailureIsAnOutageAndNeverAGrant.
//
// **The decision, stated where it is enforced.** The AP check needs a second
// controller call, and a controller can go away between the two. This driver
// fails **closed**: the answer is `PresenceUnavailable` with the error attached,
// never a pass with `APConfirmed: false`.
//
// Three reasons, and the third is the one that settles it:
//
//   - `security.md` §4 makes confirmation mandatory before a grant, and a check
//     that silently downgrades to an unchecked claim whenever one call fails is
//     a check an attacker can turn off by choosing their moment.
//   - `internal/portal`'s `Classify` already renders `PresenceUnavailable` as the
//     venue's fault rather than the guest's, so failing closed here reaches the
//     guest as *try again* and not as *your device is not on this network*.
//   - **It costs nothing.** On UniFi the grant itself is a call to the same
//     controller over the same credential. A controller that cannot answer the
//     device list cannot honour `AUTHORIZE_GUEST_ACCESS` either, so fail-open
//     would buy a grant the guest could not use. This is why the guest fail-open
//     `security.md` §2 allows for the *Policy API* does not transfer: there, RADIUS
//     can still let the guest on without Go. Here nothing can.
func TestUniFiVerifyPresence_DeviceListFailureIsAnOutageAndNeverAGrant(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  fixtureConfig
	}{
		{"the device route is not there at all", fixtureConfig{devicesFile: devicesAbsent}},
		{"the controller is up and refusing the credential", fixtureConfig{devicesStatus: http.StatusUnauthorized}},
		{"the controller answers with a server error", fixtureConfig{devicesStatus: http.StatusInternalServerError}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.flavour = FlavourUniFiOS
			cfg.clientsFile = "unifi-clients-with-uplink.json"
			fc := newFixtureController(t, cfg)
			d := fc.driver(t)

			got, err := d.VerifyPresenceDetail(context.Background(), testMAC, testAPLobbyMAC, "default")
			if err == nil {
				t.Fatalf("no error for a device list that answered %v; the caller cannot tell an "+
					"outage from a refusal", tc.name)
			}
			if got.OK {
				t.Fatal("a grant for a device whose access point could not be checked, because the " +
					"call that would have checked it failed")
			}
			if got.Outcome != PresenceUnavailable {
				t.Errorf("outcome = %q, want %q — this is not the guest's device being elsewhere, "+
					"it is the controller not answering", got.Outcome, PresenceUnavailable)
			}
			if got.APConfirmed {
				t.Error("APConfirmed on an answer nothing confirmed")
			}
		})
	}
}

// TestUniFiProbe_AccessPointWarningTracksWhatTheDriverCanActuallyDo.
//
// The warning has to be able to be **absent**, or it is a mechanism that looks
// like a check: an `access-point-unconfirmed` appended to every UniFi probe
// unconditionally gives an operator the same notice whether or not anything is
// wrong, and on a driver that checks it says the opposite of what the driver does.
//
// Both directions are asserted from one table. A warning that is always there
// and one that is never there are both green against a single-row test.
func TestUniFiProbe_AccessPointWarningTracksWhatTheDriverCanActuallyDo(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  fixtureConfig
		want bool
	}{
		{"a controller whose access points this driver can resolve", fixtureConfig{}, false},
		{"one with no device route to resolve them against", fixtureConfig{devicesFile: devicesAbsent}, true},
		{"one refusing the credential on that route", fixtureConfig{devicesStatus: http.StatusUnauthorized}, true},
		{"one whose site holds no devices at all", fixtureConfig{devicesFile: "unifi-devices-empty.json"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.flavour = FlavourUniFiOS
			fc := newFixtureController(t, cfg)
			d := fc.driver(t)

			res, err := d.Probe(context.Background())
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			_, got := findWarning(res.Warnings, WarnAccessPointUnconfirmed)
			if got != tc.want {
				t.Errorf("%q present = %v, want %v; warnings were %v",
					WarnAccessPointUnconfirmed, got, tc.want, warningKinds(res.Warnings))
			}
		})
	}
}

// TestUniFiLegacyCredentialsAreOptionalAndCostNothingWhenAbsent — the second
// credential, as a lock rather than as a paragraph.
//
// The driver does **not** require a local admin username and password alongside
// the API key: a local admin password is password-equivalent to the operator's
// whole network, asking for it in a setup form costs the install time the design
// measures, and with the access point confirmed from `uplinkDeviceId` it buys
// nothing the driver needs. The decision has one enforceable half — *nothing in the driver may treat its
// absence as an error or as a reduced capability* — and that half is here.
//
// Asserted against a driver holding **only** an API key, which is the
// configuration a stranger following the setup path actually produces. The
// comparison is against the same driver with both credentials, so the property
// is "these two answer the same", not a list of expected values that would need
// updating every time a capability moves.
func TestUniFiLegacyCredentialsAreOptionalAndCostNothingWhenAbsent(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{
		flavour:           FlavourUniFiOS,
		clientsFile:       "unifi-clients-with-uplink.json",
		issueCSRF:         testCSRFToken,
		legacyDevicesFile: "unifi-legacy-devices.json",
	})

	keyOnly := fc.driver(t)
	both := fc.driver(t, func(c *UniFiConfig) {
		c.Username = "local-admin"
		c.Password = "not-a-real-password"
	})

	if keyOnly.Capabilities() != both.Capabilities() {
		t.Errorf("capabilities differ with and without a local admin:\n  key only: %+v\n  both:     %+v\n"+
			"the second credential is optional, and a capability that moves when it is "+
			"absent is the console hiding a control over a credential nobody has to supply",
			keyOnly.Capabilities(), both.Capabilities())
	}
	if got := keyOnly.authMode(); got != "api-key" {
		t.Errorf("authMode = %q with an API key and no local admin, want api-key", got)
	}

	// The access-point check must run on the API key alone. This is the
	// assertion that would go red if the second credential were required.
	p, err := keyOnly.VerifyPresenceDetail(context.Background(), testMAC, testAPLobbyMAC, "default")
	if err != nil {
		t.Fatalf("VerifyPresenceDetail with no local admin: %v", err)
	}
	if !p.APConfirmed {
		t.Error("the access point was not confirmed for a driver holding only an API key; the " +
			"API-key-only design rests on uplinkDeviceId doing this without a second credential")
	}

	// And the probe must report the legacy phase as skipped rather than failed,
	// with no §4 notice attached: a missing optional credential is not a caveat
	// about this controller.
	res, err := keyOnly.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Errorf("probe reports errors %v for a driver with no local admin; the credential is "+
			"optional and its absence is not a failure", res.Errors)
	}
	if _, warned := findWarning(res.Warnings, WarnAccessPointUnconfirmed); warned {
		t.Errorf("the probe warns that the access point cannot be confirmed on a driver that just "+
			"confirmed one; warnings were %v", warningKinds(res.Warnings))
	}

	steps := keyOnly.RunProbe(context.Background(), UniFiProbePlan{SiteID: "default"})
	var legacy *ProbeStep
	for i := range steps {
		if strings.HasPrefix(steps[i].Name, "legacy-devices") {
			legacy = &steps[i]
		}
	}
	if legacy == nil {
		t.Fatal("the probe recorded no legacy-devices step at all; a skipped step must still say it was skipped")
	}
	if legacy.Err != "" || (legacy.Status != 0 && legacy.Status >= 400) {
		t.Errorf("the legacy phase reports a failure (%q / HTTP %d) rather than a skip", legacy.Err, legacy.Status)
	}
	// The note must not claim the legacy list is the only way to resolve an
	// access point: a sentence telling an operator to supply a network-wide
	// password asks for a credential the driver does not need.
	if strings.Contains(legacy.Note, "only known way to resolve") {
		t.Error("the skip note still calls the legacy device list the only way to resolve the " +
			"access point a redirect names; uplinkDeviceId is, and the API key already reaches it")
	}
}

// TestUniFiProbeResolvesTheSiteItWasGivenAgainstTheOnesItFound.
//
// The two-identifier-space trap, one layer down: `-site default` is the *legacy*
// site name and the Integration API answers `400 'default' is not a valid
// 'siteId' value`. A probe that asked the device list about `u.site` verbatim
// would report *no access points* for a controller with one, and the warning
// above would fire on a healthy controller — a false alarm.
//
// The fixture answers `default`, so this test pins the other half: the probe
// must prefer the site id it just discovered when the configured name is not one.
func TestUniFiProbeResolvesTheSiteItWasGivenAgainstTheOnesItFound(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{flavour: FlavourUniFiOS})
	d := fc.driver(t, func(c *UniFiConfig) { c.Site = "Default" })

	res, err := d.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(res.VendorSites) == 0 {
		t.Fatal("the probe discovered no vendor site, so this test has lost its subject")
	}
	if _, warned := findWarning(res.Warnings, WarnAccessPointUnconfirmed); warned {
		t.Errorf("the probe warned that access points cannot be confirmed on a controller that "+
			"answers the device list; it asked about the wrong site. Warnings: %v",
			warningKinds(res.Warnings))
	}
}
