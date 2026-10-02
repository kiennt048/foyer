package aaa

import (
	"context"
	"testing"
)

// TestDot1XIsNotConfigurable is the half of the failure model that is not a
// product decision.
//
// A venue may choose what happens to its guests while the control plane is down.
// It may not choose what happens to 802.1X, because failing open there puts
// unauthenticated devices on a corporate network -- which is the thing 802.1X
// exists to prevent, and is not a preference a site is entitled to.
//
// The table drives the site's *configured* value across every spelling that could
// reach it, including the ones a schema without a CHECK constraint would allow.
func TestDot1XIsNotConfigurable(t *testing.T) {
	configured := []FailureMode{
		FailOpen,
		FailClosed,
		"",
		"open",
		"FAIL-OPEN",
		"yes",
	}

	for _, mode := range configured {
		p := SiteFailurePolicy{SiteID: "site-hcm", GuestFailureMode: mode}
		if got := p.For(MethodDot1X); got != FailClosed {
			t.Errorf("802.1X with the site configured %q resolved to %q; it is fail-closed and is not the operator's to change", mode, got)
		}
	}
}

// TestGuestModeIsTheOperatorsAndAnythingElseIsClosed pins the other half: the
// operator's choice is honoured exactly when it is one of the two defined modes,
// and every other value -- a NULL read as "", a typo, a value from a schema written
// after this code -- resolves closed.
//
// The convenient coercion here is "open", which is why it is asserted rather than
// assumed.
func TestGuestModeIsTheOperatorsAndAnythingElseIsClosed(t *testing.T) {
	cases := map[FailureMode]FailureMode{
		FailOpen:    FailOpen,
		FailClosed:  FailClosed,
		"":          FailClosed,
		"open":      FailClosed,
		"FAIL-OPEN": FailClosed,
		"true":      FailClosed,
	}

	for configured, want := range cases {
		p := SiteFailurePolicy{SiteID: "site-hcm", GuestFailureMode: configured}
		if got := p.For(MethodGuest); got != want {
			t.Errorf("guest with the site configured %q resolved to %q, want %q", configured, got, want)
		}
	}
}

// TestDefaultPolicyIsClosed: every way of not knowing which site a request belongs
// to is a way of not knowing whose network it is.
func TestDefaultPolicyIsClosed(t *testing.T) {
	p := DefaultPolicy("")
	if p.For(MethodGuest) != FailClosed || p.For(MethodDot1X) != FailClosed {
		t.Fatalf("an unconfigured site must be closed on both paths, got guest=%q dot1x=%q",
			p.For(MethodGuest), p.For(MethodDot1X))
	}
}

// TestAccessMethodIsEAPPresenceAndNothingElse takes the narrow reading: 802.1X is
// exactly "the request carries EAP".
//
// The Service-Type cases are the ones that matter. A heuristic that reads
// Framed-User as 802.1X would classify a MikroTik guest login as a supplicant and
// silently take a fail-open site closed; one that reads Call-Check as guest would
// be right today and wrong the first time a vendor sends it inside EAP.
func TestAccessMethodIsEAPPresenceAndNothingElse(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want AccessMethod
	}{
		{"bare voucher login", Request{UserName: "VALID-123"}, MethodGuest},
		{"MAC auth, Call-Check", Request{UserName: "aabbccddeeff", ServiceType: "Call-Check"}, MethodGuest},
		{"portal login with Framed-User", Request{UserName: "reception", ServiceType: "Framed-User"}, MethodGuest},
		{"EAP present", Request{UserName: "linh", EAPMessage: "present"}, MethodDot1X},
		{"EAP present and Service-Type says otherwise", Request{UserName: "linh", EAPMessage: "1", ServiceType: "Call-Check"}, MethodDot1X},
		{"EAP field whitespace only", Request{UserName: "linh", EAPMessage: "   "}, MethodGuest},
	}

	for _, tc := range cases {
		if got := tc.req.AccessMethod(); got != tc.want {
			t.Errorf("%s: classified %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestGraceGrantCarriesNothingItWouldHaveHadToLookUp.
//
// A fail-open Access-Accept is minted while the thing that knows the answers is
// unreachable, so every attribute on it that would normally have come from a
// lookup is a guess. Emitting a guessed rate limit is worse than emitting none: it
// is the admin console's rate-limit field silently not applying, which is exactly
// the failure `docs/rules/frontend.md` forbids on the display side.
//
// The assertion is on the shape of the whole map, not on the presence of three
// keys, so an attribute added later has to be argued for here.
func TestGraceGrantCarriesNothingItWouldHaveHadToLookUp(t *testing.T) {
	p := SiteFailurePolicy{SiteID: "site-hcm", GuestFailureMode: FailOpen, GuestGraceSeconds: 600}
	attrs := p.GraceAttributes(300)

	want := map[string]string{
		"Session-Timeout":       "600",
		"Acct-Interim-Interval": "300",
		"Class":                 DegradedClass,
	}
	if len(attrs) != len(want) {
		t.Fatalf("a degraded grant carries %d attributes %v; it may carry exactly %v", len(attrs), attrs, want)
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("degraded grant %s = %q, want %q", k, attrs[k], v)
		}
	}

	// Named individually because these are the ones somebody will be tempted to
	// add back: they are the attributes a normal accept carries.
	for _, forbidden := range []string{"Filter-Id", "Tunnel-Private-Group-Id", "WISPr-Bandwidth-Max-Down", "Idle-Timeout"} {
		if _, ok := attrs[forbidden]; ok {
			t.Errorf("a degraded grant carries %s, which nothing looked up", forbidden)
		}
	}
}

// TestGraceIsBounded: a blip of thirty seconds must not mint an eight-hour session.
func TestGraceIsBounded(t *testing.T) {
	cases := map[int]int{
		0:      DefaultGraceSeconds,
		-1:     DefaultGraceSeconds,
		60:     60,
		600:    600,
		999999: MaxGraceSeconds,
	}
	for configured, want := range cases {
		p := SiteFailurePolicy{GuestGraceSeconds: configured}
		if got := p.GraceSeconds(); got != want {
			t.Errorf("grace configured %d resolved to %d, want %d", configured, got, want)
		}
	}
}

// TestAdviceNamesBothBranches.
//
// `docs/rules/security.md` §2: *"Never leave it to unlang's default."* A
// generated config that carries only the configurable half has not declared the
// other one -- it has inherited whichever branch happened to be last. Advice is
// what the generator renders, so it has to carry both even though one of them is
// never anything but fail-closed.
func TestAdviceNamesBothBranches(t *testing.T) {
	open := SiteFailurePolicy{SiteID: "site-hcm", GuestFailureMode: FailOpen}.Advice(300)
	if open.Dot1X != FailClosed {
		t.Errorf("advice for a fail-open site names dot1x %q", open.Dot1X)
	}
	if open.Guest != FailOpen {
		t.Errorf("advice for a fail-open site names guest %q", open.Guest)
	}
	if len(open.GraceAttributes) == 0 {
		t.Error("a fail-open site's advice carries no grace attributes, so the generator has nothing to emit on the accept")
	}

	closed := SiteFailurePolicy{SiteID: "site-fac", GuestFailureMode: FailClosed}.Advice(300)
	if closed.Dot1X != FailClosed || closed.Guest != FailClosed {
		t.Errorf("advice for a fail-closed site: dot1x=%q guest=%q", closed.Dot1X, closed.Guest)
	}
	if len(closed.GraceAttributes) != 0 {
		t.Errorf("a fail-closed site's advice carries grace attributes %v, which nothing will ever send", closed.GraceAttributes)
	}
}

type policyStoreWithSites struct {
	mockStore
	policies []SiteFailurePolicy
}

func (p *policyStoreWithSites) SiteFailurePolicies(context.Context) ([]SiteFailurePolicy, error) {
	return p.policies, nil
}

// TestFailurePoliciesSaysWhereTheAnswerCameFrom.
//
// "Every site is fail-closed" and "nothing is configured, so every site is
// fail-closed" render identically in a generated config and in a doctor report, and
// they are not the same fact to the operator reading it. One is a choice; the other
// is a column that does not exist yet.
func TestFailurePoliciesSaysWhereTheAnswerCameFrom(t *testing.T) {
	plain := &mockStore{}
	advice, configured, err := FailurePolicies(context.Background(), plain, 300)
	if err != nil {
		t.Fatal(err)
	}
	if configured {
		t.Error("a store with no site failure policies reported itself as configured")
	}
	if len(advice) != 0 {
		t.Errorf("a store with no site failure policies produced advice %v", advice)
	}

	withSites := &policyStoreWithSites{policies: []SiteFailurePolicy{
		{SiteID: "site-hcm", GuestFailureMode: FailOpen},
		{SiteID: "site-fac", GuestFailureMode: FailClosed},
	}}
	advice, configured, err = FailurePolicies(context.Background(), withSites, 300)
	if err != nil {
		t.Fatal(err)
	}
	if !configured {
		t.Error("a store implementing FailurePolicyStore reported itself as unconfigured")
	}
	if len(advice) != 2 {
		t.Fatalf("got %d pieces of advice, want 2", len(advice))
	}
	if advice[0].Guest != FailOpen || advice[1].Guest != FailClosed {
		t.Errorf("advice did not follow the configured sites: %+v", advice)
	}
}
