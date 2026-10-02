package aaa

import (
	"context"
	"fmt"
	"strings"
)

// The per-site failure model: what the network does while the Policy API cannot
// answer.
//
// The decision this file encodes has one property that shapes everything else in
// it: **the Policy API cannot be the thing that applies it.** By the time the
// question is being asked the Go process is unreachable, so anything it would have
// said is unavailable. The failure behaviour therefore has to be pre-positioned in
// FreeRADIUS's own configuration, which means it is generated -- `internal/raddb`
// renders Advice into unlang -- and it means changing a site's failure mode is a
// `restart`-class change rather than a `sql-only` one.
//
// `docs/rules/security.md` §2 adds the part that is easy to skip: *"Never leave
// it to unlang's default."* A configuration that reaches its failure behaviour
// through an `else` has not chosen one; it has inherited whichever branch happened
// to be last. The generated config must name the failure branch explicitly, per
// site, and the operator must be able to read which one they are getting.

// FailureMode is what happens to an authentication the Policy API could not answer.
type FailureMode string

const (
	// FailClosed rejects. Nobody new gets on the network while the control plane
	// is down; sessions already established are untouched, because they are not
	// re-authenticating.
	FailClosed FailureMode = "fail-closed"

	// FailOpen accepts, with a bounded grant carrying nothing the Policy API
	// would have had to look up. See GraceAttributes.
	FailOpen FailureMode = "fail-open"
)

// Valid reports whether m is one of the two defined modes. Anything else -- an
// empty string, a typo in a database column, a value from a newer schema -- is not
// silently coerced to a mode, because the coercion that is convenient here is
// "open".
func (m FailureMode) Valid() bool { return m == FailClosed || m == FailOpen }

// AccessMethod is the distinction that decides whether the operator gets a choice.
type AccessMethod string

const (
	// MethodDot1X is 802.1X: the request carries EAP. The failure mode is
	// fail-closed and is not configurable -- failing open here puts
	// unauthenticated devices on a corporate network, which is not a venue
	// preference, it is the thing 802.1X exists to prevent.
	MethodDot1X AccessMethod = "dot1x"

	// MethodGuest is everything else: the captive portal, MAC authentication,
	// voucher and account logins. A hotel that would rather let strangers on than
	// have a lobby full of people who cannot connect, and a factory that would
	// rather nobody connected, are both right about their own site.
	MethodGuest AccessMethod = "guest"
)

// SiteFailurePolicy is one site's answer, as the operator configured it.
//
// The columns this comes from do not exist in `internal/store` yet; see
// FailurePolicyStore for the shape required of them. Until they do, DefaultPolicy
// is what a site gets, and it is fail-closed.
type SiteFailurePolicy struct {
	SiteID string

	// GuestFailureMode applies to MethodGuest only. Whatever it says, 802.1X is
	// fail-closed -- see For.
	GuestFailureMode FailureMode

	// GuestGraceSeconds bounds a fail-open grant. It is a Session-Timeout, so the
	// device re-authenticates when it expires: the moment the Policy API is back,
	// the next re-authentication is a real decision rather than another free pass.
	//
	// A blip of thirty seconds must not mint an eight-hour session.
	GuestGraceSeconds int
}

// DefaultGraceSeconds is 300, matching the widely-implemented Acct-Interim-Interval
// floor: the first accounting sample of a degraded session is also the moment it
// has to ask again.
const DefaultGraceSeconds = 300

// MaxGraceSeconds caps a fail-open grant at an hour. A grace period longer than the
// outages it covers stops being a grace period: it is a site that authorizes nobody
// and lets everybody on.
const MaxGraceSeconds = 3600

// DegradedClass is set as the RADIUS Class attribute on a fail-open grant.
//
// RFC 2865 §5.25 requires the NAS to echo Class unmodified in its accounting
// packets, so the sessions a site let on while blind are identifiable in `radacct`
// afterwards rather than indistinguishable from authorized ones. DOCUMENTED from
// the RFC and not measured against any NAS -- check docs/protocol-reality.md before
// relying on a particular vendor honouring it.
const DegradedClass = "Foyer:degraded"

// DefaultPolicy is what an unconfigured, unknown or unparseable site gets.
//
// Fail-closed, and deliberately: every way of not knowing which site a request
// belongs to is a way of not knowing whose network it is.
func DefaultPolicy(siteID string) SiteFailurePolicy {
	return SiteFailurePolicy{
		SiteID:            siteID,
		GuestFailureMode:  FailClosed,
		GuestGraceSeconds: DefaultGraceSeconds,
	}
}

// For resolves the failure mode for one access method.
//
// MethodDot1X ignores GuestFailureMode entirely. That is the whole rule, and it is
// a method on the policy rather than a check at each call site so that there is no
// call site able to get it wrong.
func (p SiteFailurePolicy) For(method AccessMethod) FailureMode {
	if method == MethodDot1X {
		return FailClosed
	}
	if !p.GuestFailureMode.Valid() {
		return FailClosed
	}
	return p.GuestFailureMode
}

// GraceSeconds is the bounded lifetime of a fail-open grant, floored and capped.
func (p SiteFailurePolicy) GraceSeconds() int {
	switch {
	case p.GuestGraceSeconds <= 0:
		return DefaultGraceSeconds
	case p.GuestGraceSeconds > MaxGraceSeconds:
		return MaxGraceSeconds
	default:
		return p.GuestGraceSeconds
	}
}

// GraceAttributes are the reply attributes a fail-open Access-Accept may carry.
//
// It is a short list on purpose. Every attribute the Policy API would normally
// supply -- a rate limit from the voucher batch, a VLAN from the account, a real
// Session-Timeout -- is a value that had to be looked up, and the lookup is exactly
// what is unavailable. Emitting a remembered or guessed one is worse than emitting
// none: it is the admin console's rate-limit field silently not applying.
//
// So: how long, how often to report, and a mark saying this was not a decision.
func (p SiteFailurePolicy) GraceAttributes(interimInterval int) map[string]string {
	if interimInterval < DefaultGraceSeconds {
		interimInterval = DefaultGraceSeconds
	}
	return map[string]string{
		"Session-Timeout":       fmt.Sprintf("%d", p.GraceSeconds()),
		"Acct-Interim-Interval": fmt.Sprintf("%d", interimInterval),
		"Class":                 DegradedClass,
	}
}

// Advice is the resolved, renderable form of a site's failure model: what
// `internal/raddb` has to emit into the generated virtual server, and what
// `foyerctl` shows an operator who asks what happens when the control plane stops.
//
// Dot1X is present even though it is always FailClosed. A generated config that
// carries only the configurable half leaves the other half to unlang, which is the
// thing §2 forbids.
type Advice struct {
	SiteID          string            `json:"siteId"`
	Dot1X           FailureMode       `json:"dot1x"`
	Guest           FailureMode       `json:"guest"`
	GraceSeconds    int               `json:"graceSeconds"`
	GraceAttributes map[string]string `json:"graceAttributes,omitempty"`
}

// Advice resolves the policy into the form the generator consumes.
func (p SiteFailurePolicy) Advice(interimInterval int) Advice {
	a := Advice{
		SiteID:       p.SiteID,
		Dot1X:        p.For(MethodDot1X),
		Guest:        p.For(MethodGuest),
		GraceSeconds: p.GraceSeconds(),
	}
	if a.Guest == FailOpen {
		a.GraceAttributes = p.GraceAttributes(interimInterval)
	}
	return a
}

// FailurePolicyStore is the shape `internal/store` has to grow for this to be
// operator-configurable. It is deliberately a read of every site at once: the
// caller is the config generator, which needs all of them to emit one virtual
// server, not a lookup on the request path.
//
// The columns this reads, landed in migration 00007 and renamed in 00009:
//
//	ALTER TABLE sites
//	    ADD COLUMN guest_policy_failure_mode text NOT NULL DEFAULT 'fail-closed'
//	        CHECK (guest_policy_failure_mode IN ('fail-closed', 'fail-open')),
//	    ADD COLUMN guest_policy_failure_grace_seconds integer NOT NULL DEFAULT 300
//	        CHECK (guest_policy_failure_grace_seconds BETWEEN 60 AND 3600);
//
// **`guest_`, because that is the whole of what they govern.** SiteFailurePolicy.For
// ignores GuestFailureMode entirely for MethodDot1X -- a method rather than a check
// at each call site, precisely so that no call site can get it wrong. SQL has no
// For(). An ad-hoc query, a future admin API and a support engineer reading
// `\d sites` all see the column and none of them sees the method, and a wide name
// for a narrow fact invites exactly the wide reading it does not mean.
// There is deliberately no column for the 802.1X answer, because it is not
// configurable: a venue may choose to wave guests through while the control plane
// is blind; it may not choose that for a corporate supplicant.
//
// `NOT NULL DEFAULT 'fail-closed'` rather than a nullable column: a site that has
// never been configured must not be a site whose failure mode is NULL, because
// every reader then has to decide what NULL means and one of them will decide it
// means "unset, so allow".
type FailurePolicyStore interface {
	SiteFailurePolicies(ctx context.Context) ([]SiteFailurePolicy, error)
}

// FailurePolicies resolves every site's advice, and says where the answer came
// from.
//
// The boolean is not decoration. "Every site is fail-closed" and "nothing is
// configured, so every site is fail-closed" render identically and are not the same
// fact to an operator reading a generated config or a doctor report.
func FailurePolicies(ctx context.Context, store any, interimInterval int) (advice []Advice, configured bool, err error) {
	fps, ok := store.(FailurePolicyStore)
	if !ok || fps == nil {
		return nil, false, nil
	}
	policies, err := fps.SiteFailurePolicies(ctx)
	if err != nil {
		return nil, true, err
	}
	out := make([]Advice, 0, len(policies))
	for _, p := range policies {
		out = append(out, p.Advice(interimInterval))
	}
	return out, true, nil
}

// AccessMethod classifies a request.
//
// The narrow reading: 802.1X is exactly "the request carries
// EAP". Not "it looks corporate", not a Service-Type heuristic -- an EAP-Message
// attribute is the thing that makes a supplicant a supplicant, and everything
// without one is on the guest path where the operator has a choice.
//
// Note what this is *not* used for on the live path: the classification that
// matters happens inside FreeRADIUS, in the generated config, while the Policy API
// is unreachable. This function exists so the two agree, and so the rule can be
// asserted without a RADIUS server.
func (r Request) AccessMethod() AccessMethod {
	if strings.TrimSpace(r.EAPMessage) != "" {
		return MethodDot1X
	}
	return MethodGuest
}
