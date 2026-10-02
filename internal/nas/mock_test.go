package nas

import (
	"context"
	"errors"
	"testing"
)

// What the fake can produce, measured against what the type declares.
//
// Two defects with one shape are guarded against here: a stand-in more agreeable
// than the thing it stands in for — one that cannot produce every presence
// outcome, or one that cannot fail — leaves every branch the product has for a
// refusal, an outage or a revocation that failed with no route into it from the
// shipped mock. The usual workaround is a hand-rolled fake per test, and a
// duplicate fake is a second copy of an assumption — the same way the original
// goes wrong.

// allPresenceOutcomes is every value the type declares. It is a list rather than
// a count so that adding a seventh outcome fails this file rather than passing
// it: the guarantee is "the fake can produce whatever a caller may have to
// handle", and that guarantee is only as good as its lower bound being derived.
var allPresenceOutcomes = []PresenceOutcome{
	PresenceVerified,
	PresenceNotFound,
	PresenceAlreadyAuthorized,
	PresenceSiteMismatch,
	PresenceAPMismatch,
	PresenceUnavailable,
}

const (
	mockGuestMAC = "1c:71:25:63:e4:24"
	mockAPMAC    = "00:11:22:33:44:55"
	mockSite     = "site-lobby"
)

// TestMockDriverProducesEveryPresenceOutcome locks the first.
//
// It asserts coverage of the declared set, not of six hand-listed cases: a fake
// that can produce five of six is exactly the situation this replaces, and the
// only way to notice is to compare against the type.
func TestMockDriverProducesEveryPresenceOutcome(t *testing.T) {
	cases := []struct {
		name    string
		driver  *MockDriver
		apMAC   string
		siteID  string
		want    PresenceOutcome
		wantOK  bool
		wantErr bool
	}{
		{
			name:   "a client the controller knows, unauthorized, on the AP it claims",
			driver: NewStrictMockDriver(VendorUniFi).WithClient(mockGuestMAC, MockClient{ID: "c-1", SiteID: mockSite, APMAC: mockAPMAC}),
			apMAC:  mockAPMAC, siteID: mockSite,
			want: PresenceVerified, wantOK: true,
		},
		{
			name:   "a MAC the controller has never seen",
			driver: NewStrictMockDriver(VendorUniFi),
			siteID: mockSite,
			want:   PresenceNotFound,
		},
		{
			// Not a refusal. A guest who is already online being told the device
			// is not on the network is the failure this outcome exists to stop.
			name:   "a client the controller has already let in",
			driver: NewStrictMockDriver(VendorUniFi).WithClient(mockGuestMAC, MockClient{ID: "c-1", SiteID: mockSite, Authorized: true}),
			siteID: mockSite,
			want:   PresenceAlreadyAuthorized,
		},
		{
			name:   "the redirect claims a site the controller does not have this client in",
			driver: NewStrictMockDriver(VendorUniFi).WithClient(mockGuestMAC, MockClient{ID: "c-1", SiteID: "site-cafe"}),
			siteID: mockSite,
			want:   PresenceSiteMismatch,
		},
		{
			name:   "the redirect claims an access point the controller does not agree with",
			driver: NewStrictMockDriver(VendorUniFi).WithClient(mockGuestMAC, MockClient{ID: "c-1", SiteID: mockSite, APMAC: mockAPMAC}),
			apMAC:  "aa:bb:cc:dd:ee:ff", siteID: mockSite,
			want: PresenceAPMismatch,
		},
		{
			name:   "the controller cannot be reached",
			driver: NewStrictMockDriver(VendorUniFi).WithControllerUnreachable(nil),
			siteID: mockSite,
			want:   PresenceUnavailable, wantErr: true,
		},
	}

	produced := make(map[PresenceOutcome]bool, len(cases))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tc.driver.VerifyPresenceDetail(context.Background(), mockGuestMAC, tc.apMAC, tc.siteID)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if p.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", p.Outcome, tc.want)
			}
			if p.OK != tc.wantOK {
				t.Errorf("OK = %v, want %v for outcome %q", p.OK, tc.wantOK, p.Outcome)
			}
			produced[p.Outcome] = true
		})
	}

	for _, want := range allPresenceOutcomes {
		if !produced[want] {
			t.Errorf("the shipped mock cannot produce %q, so no test can reach the branch that handles it", want)
		}
	}
}

// TestMockUnavailableCarriesAnErrorBesideTheOutcome.
//
// Both halves, because a caller reading one of them classifies an outage as a
// refusal half the time — which is how the three situations collapsed into one
// sentence in `internal/api` and why a second fake was written there.
func TestMockUnavailableCarriesAnErrorBesideTheOutcome(t *testing.T) {
	down := errors.New("dial tcp 192.0.2.1:443: connect: connection refused")
	d := NewMockDriver(VendorUniFi).WithControllerUnreachable(down)

	p, err := d.VerifyPresenceDetail(context.Background(), mockGuestMAC, "", mockSite)
	if !errors.Is(err, down) {
		t.Fatalf("err = %v, want the transport failure the test supplied", err)
	}
	if p.Outcome != PresenceUnavailable {
		t.Errorf("outcome = %q, want %q", p.Outcome, PresenceUnavailable)
	}
	if p.OK {
		t.Error("OK is true for a controller that never answered")
	}

	// DefaultPresent is true here on purpose: an unreachable controller must
	// beat the permissive default, or the outage reads as a successful
	// verification and a grant is issued for a device nobody confirmed.
	ok, err := d.VerifyPresence(context.Background(), mockGuestMAC, "", mockSite)
	if ok || err == nil {
		t.Errorf("VerifyPresence = (%v, %v) against an unreachable controller", ok, err)
	}
}

// TestMockAPConfirmedIsNotAssumed: the mock reads apMAC, and reporting a
// confirmation it did not make would be worse than ignoring the argument.
func TestMockAPConfirmedIsNotAssumed(t *testing.T) {
	cases := []struct {
		name        string
		controllerA string
		claimedAP   string
		wantConfirm bool
	}{
		{"both sides name an AP and they agree", mockAPMAC, mockAPMAC, true},
		{"the controller knows of no AP — the UniFi Integration API's own case", "", mockAPMAC, false},
		{"the redirect claimed no AP", mockAPMAC, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewStrictMockDriver(VendorUniFi).
				WithClient(mockGuestMAC, MockClient{SiteID: mockSite, APMAC: tc.controllerA})
			p, err := d.VerifyPresenceDetail(context.Background(), mockGuestMAC, tc.claimedAP, mockSite)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !p.OK {
				t.Fatalf("outcome = %q, want a pass", p.Outcome)
			}
			if p.APConfirmed != tc.wantConfirm {
				t.Errorf("APConfirmed = %v, want %v", p.APConfirmed, tc.wantConfirm)
			}
		})
	}
}

// TestMockDeauthorizeCanFail locks the second.
//
// Every one of these was nil before, so /problems/session-end-failed had no
// mock-driven route to it at all.
func TestMockDeauthorizeCanFail(t *testing.T) {
	refusal := MockAPIRefusal(409, "no such session")

	cases := []struct {
		name    string
		driver  *MockDriver
		req     DeauthorizeRequest
		wantErr bool
		// wantAPI asks whether the failure is the shape a driver checks before
		// deciding a CoA fallback is justified.
		wantAPI bool
	}{
		{
			name:   "an authorized client is revoked",
			driver: NewMockDriver(VendorUniFi),
			req:    DeauthorizeRequest{ClientMAC: mockGuestMAC, SiteID: mockSite},
		},
		{
			name:    "no MAC at all",
			driver:  NewMockDriver(VendorUniFi),
			req:     DeauthorizeRequest{SiteID: mockSite},
			wantErr: true,
		},
		{
			name:    "a client this controller has never seen",
			driver:  NewStrictMockDriver(VendorUniFi),
			req:     DeauthorizeRequest{ClientMAC: mockGuestMAC, SiteID: mockSite},
			wantErr: true,
		},
		{
			name:    "the controller is unreachable — the only failure that justifies a CoA fallback",
			driver:  NewMockDriver(VendorUniFi).WithControllerUnreachable(nil),
			req:     DeauthorizeRequest{ClientMAC: mockGuestMAC, SiteID: mockSite},
			wantErr: true,
		},
		{
			name: "the controller answered and refused — a fallback here would act on our own opinion of its answer",
			driver: NewMockDriver(VendorUniFi).WithFaults(func(f *MockFaults) {
				f.Deauthorize = refusal
			}),
			req:     DeauthorizeRequest{ClientMAC: mockGuestMAC, SiteID: mockSite},
			wantErr: true, wantAPI: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.driver.DeauthorizeGuest(context.Background(), tc.req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("DeauthorizeGuest err = %v, wantErr = %v", err, tc.wantErr)
			}
			var apiErr *APIError
			if got := errors.As(err, &apiErr); got != tc.wantAPI {
				t.Errorf("errors.As(*APIError) = %v, want %v — the two failures are not interchangeable", got, tc.wantAPI)
			}
		})
	}
}

// TestMockDeauthorizeRevokesAClientThatIsAlreadyAuthorized: the refusal must key
// on "this controller has never seen it", never on presence.OK. An
// already-authorized client answers OK == false and is precisely the one a
// revocation is for.
func TestMockDeauthorizeRevokesAClientThatIsAlreadyAuthorized(t *testing.T) {
	d := NewStrictMockDriver(VendorUniFi).
		WithClient(mockGuestMAC, MockClient{ID: "c-1", SiteID: mockSite, Authorized: true})

	if err := d.DeauthorizeGuest(context.Background(), DeauthorizeRequest{
		ClientMAC: mockGuestMAC, SiteID: mockSite, Reason: "quota exhausted",
	}); err != nil {
		t.Fatalf("revoking an already-authorized client failed: %v", err)
	}
	if got := d.Deauthorized[mockGuestMAC]; got != "quota exhausted" {
		t.Errorf("Deauthorized[%s] = %q", mockGuestMAC, got)
	}
}

// TestMockDiscoverAndProbeCanFail: the remaining two calls that could not.
func TestMockDiscoverAndProbeCanFail(t *testing.T) {
	boom := errors.New("mock: the controller refused the credential")

	t.Run("discovery", func(t *testing.T) {
		d := NewMockDriver(VendorUniFi).WithFaults(func(f *MockFaults) { f.Discover = boom })
		aps, err := d.DiscoverAccessPoints(context.Background())
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the injected failure", err)
		}
		if aps != nil {
			t.Errorf("a failed discovery returned %d access points", len(aps))
		}
	})

	t.Run("probe still returns diagnostics beside the error", func(t *testing.T) {
		d := NewMockDriver(VendorUniFi).WithControllerUnreachable(boom)
		res, err := d.Probe(context.Background())
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the injected failure", err)
		}
		if res == nil {
			t.Fatal("no ProbeResult, so a registration screen has an error string where the diagnostics belong")
		}
		if res.Reachable {
			t.Error("Reachable is true for a controller nobody could reach")
		}
	})
}

// TestMockAuthorizeCanFail closes the third of the four action calls.
func TestMockAuthorizeCanFail(t *testing.T) {
	boom := errors.New("mock: authorization refused")
	d := NewMockDriver(VendorUniFi).WithFaults(func(f *MockFaults) { f.Authorize = boom })

	if err := d.AuthorizeGuest(context.Background(), AuthorizeRequest{ClientMAC: mockGuestMAC}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	if _, recorded := d.Authorized[mockGuestMAC]; recorded {
		t.Error("a failed authorization was recorded as one that happened")
	}
}

// TestMockClientsDoNotDisturbTheExistingDefaults.
//
// The additions are opt-in, and every caller already depending on
// NewMockDriver's permissive default must be untouched by them — otherwise this
// is a rewrite of four packages' fixtures wearing a fake's clothes.
func TestMockClientsDoNotDisturbTheExistingDefaults(t *testing.T) {
	permissive := NewMockDriver(VendorUniFi)
	if p, err := permissive.VerifyPresenceDetail(context.Background(), "de:ad:be:ef:00:01", mockAPMAC, mockSite); err != nil || !p.OK {
		t.Errorf("NewMockDriver no longer answers present for an unlisted MAC: (%+v, %v)", p, err)
	}

	strict := NewStrictMockDriver(VendorUniFi)
	if p, _ := strict.VerifyPresenceDetail(context.Background(), "de:ad:be:ef:00:01", mockAPMAC, mockSite); p.OK {
		t.Error("NewStrictMockDriver answered present for an unlisted MAC")
	}

	// Presences still wins over DefaultPresent, and Clients still wins over both.
	listed := NewMockDriver(VendorUniFi)
	listed.Presences["de:ad:be:ef:00:02"] = false
	if p, _ := listed.VerifyPresenceDetail(context.Background(), "de:ad:be:ef:00:02", "", mockSite); p.OK {
		t.Error("an explicit Presences=false was overridden by DefaultPresent")
	}
}
