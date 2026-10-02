package raddb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/aaa"
)

// advice is the shortest honest way to build one, and it goes through
// `internal/aaa` rather than composing an aaa.Advice literal here.
//
// Deliberate, per `docs/rules/database.md`'s "derive the stand-in from what it
// stands in for": a literal in this file would be a second copy of the resolution
// rules in failure.go -- the dot1x override, the grace clamp, which attributes a
// fail-open grant may carry -- and it would go on passing on the day one of them
// changed, describing a policy `aaa` no longer produces.
func advice(t *testing.T, siteID string, mode aaa.FailureMode, grace, interim int) aaa.Advice {
	t.Helper()
	p := aaa.SiteFailurePolicy{SiteID: siteID, GuestFailureMode: mode, GuestGraceSeconds: grace}
	return p.Advice(interim)
}

func failureConfig(t *testing.T, adv ...aaa.Advice) Config {
	t.Helper()
	c := DefaultConfig()
	c.PostgresPassword = "not-a-default"
	c.GuestFailureAdvice = adv
	return c
}

// An empty advice set is not "unconfigured means open".
func TestNoAdviceIsFailClosedAndSaysSo(t *testing.T) {
	c := failureConfig(t)
	if err := NewGenerator(c).Validate(); err != nil {
		t.Fatalf("a config with no site advice must generate: %v", err)
	}
	if c.GuestFailsOpen() {
		t.Fatal("no site configuration resolved to fail-OPEN. Unconfigured must never mean open.")
	}
	if c.GuestFailureConfigured() {
		t.Error("an empty advice set reported itself as configured")
	}
	// "Every site is fail-closed" and "nothing is configured, so every site is
	// fail-closed" render identically in unlang and are not the same fact to an
	// operator reading the tree.
	if !strings.Contains(c.GuestFailureSummary(), "DefaultPolicy") {
		t.Errorf("the summary does not say the answer came from the default: %q", c.GuestFailureSummary())
	}
}

func TestOneFailOpenSiteReachesTheGeneratedTree(t *testing.T) {
	c := failureConfig(t, advice(t, "hanoi", aaa.FailOpen, 600, 300))
	if err := NewGenerator(c).Validate(); err != nil {
		t.Fatalf("one fail-open site must generate: %v", err)
	}
	if !c.GuestFailsOpen() {
		t.Fatal("a site configured fail-open did not resolve to fail-open")
	}
	if got := c.GuestGraceSeconds(); got != 600 {
		t.Errorf("grace seconds = %d, want 600", got)
	}
}

// The two halves that must not be satisfied by one of them.
//
// A per-site setting that produces identical unlang on both settings is the entire
// defect this exists to prevent, so the trees are compared to each other rather
// than each to a hardcoded string.
func TestTheTwoModesGenerateDifferentUnlang(t *testing.T) {
	closed := generateDefaultSite(t, failureConfig(t, advice(t, "s", aaa.FailClosed, 300, 300)))
	open := generateDefaultSite(t, failureConfig(t, advice(t, "s", aaa.FailOpen, 600, 300)))

	if closed == open {
		t.Fatal("fail-closed and fail-open generated identical virtual servers. " +
			"A setting that changes no byte of the configuration changes no behaviour.")
	}

	// And the difference must be the one that matters, not a comment. Strip every
	// comment line and compare the unlang itself -- otherwise this test is
	// satisfied by the generated summary line differing.
	if stripComments(closed) == stripComments(open) {
		t.Fatal("fail-closed and fail-open differ only in comments. The unlang is the same, " +
			"so the generated server does the same thing under both settings.")
	}

	if !strings.Contains(open, `Class := "Foyer:degraded"`) {
		t.Error("the fail-open branch does not mark the grant. Without Class, the sessions a site let on " +
			"while blind are indistinguishable from authorized ones in radacct (RFC 2865 §5.25).")
	}
	if !strings.Contains(open, `Session-Timeout := "600"`) {
		t.Error("the fail-open branch carries no Session-Timeout. An unbounded grace grant means a " +
			"thirty-second blip mints an eight-hour session.")
	}
	if strings.Contains(closed, "Auth-Type := Accept\n\t\t\t\t}") && !strings.Contains(closed, "Reply-Message") {
		t.Error("the fail-closed branch accepts")
	}
}

// 802.1X is excluded whatever the site says, and this asserts it on the fail-OPEN
// tree specifically -- which is the only tree where the exclusion can fail.
//
// A fail branch that rejects everything keeps 802.1X closed by construction
// rather than by declaration. That stops being safe the moment the guest half is
// configurable, which it is.
func TestFailOpenDoesNotReachDot1X(t *testing.T) {
	open := generateDefaultSite(t, failureConfig(t, advice(t, "s", aaa.FailOpen, 600, 300)))

	// The EAP arm of the fail branch, up to the else that starts the guest arm.
	i := strings.Index(open, "if (&EAP-Message) {")
	if i < 0 {
		t.Fatal("the generated fail branch no longer classifies on EAP-Message presence")
	}
	// The second occurrence is the one inside the fail branch; the first sets
	// Tmp-String-0 before the rest call.
	j := strings.Index(open[i+1:], "if (&EAP-Message) {")
	if j < 0 {
		t.Fatal("the fail branch has no EAP arm")
	}
	arm := open[i+1+j:]
	if end := strings.Index(arm, "\t\t\telse {"); end > 0 {
		arm = arm[:end]
	}
	if strings.Contains(arm, "Auth-Type := Accept") {
		t.Fatalf("the 802.1X arm of the fail branch accepts on a fail-open site. "+
			"There is no degraded 802.1X accept: the supplicant expects keying material this server "+
			"cannot invent, and failing open here puts unauthenticated devices on a corporate network.\n%s", arm)
	}
	if !strings.Contains(arm, "Auth-Type := Reject") {
		t.Fatalf("the 802.1X arm of the fail branch does not reject:\n%s", arm)
	}
}

// A site asking for a fail-open 802.1X is a caller defect, and a defect the tree
// would hide is refused rather than quietly narrowed.
func TestDot1XFailOpenIsRefused(t *testing.T) {
	c := failureConfig(t)
	c.GuestFailureAdvice = []aaa.Advice{{SiteID: "s", Dot1X: aaa.FailOpen, Guest: aaa.FailClosed, GraceSeconds: 300}}
	err := NewGenerator(c).Validate()
	if err == nil {
		t.Fatal("a site asking for fail-open 802.1X was accepted")
	}
	if !strings.Contains(err.Error(), "802.1X is fail-closed") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// The refusal that stands in for the routing this generator does not build.
func TestDisagreeingSitesAreRefusedByName(t *testing.T) {
	c := failureConfig(t,
		advice(t, "hanoi", aaa.FailOpen, 600, 300),
		advice(t, "factory", aaa.FailClosed, 300, 300),
		advice(t, "warehouse", aaa.FailClosed, 300, 300),
	)
	err := NewGenerator(c).Validate()
	if err == nil {
		t.Fatal("a mixed advice set generated. The fail-open site would have quietly failed closed, " +
			"which is the operator's setting silently not applying.")
	}
	// Both sides named, because "the sites disagree" is not something an operator
	// can act on.
	for _, want := range []string{"hanoi", "factory", "warehouse", "fail-open", "fail-closed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// Two sites that agree are one advice, so a hundred fail-open sites still generate.
func TestAgreeingSitesCollapse(t *testing.T) {
	c := failureConfig(t,
		advice(t, "a", aaa.FailOpen, 600, 300),
		advice(t, "b", aaa.FailOpen, 600, 300),
	)
	if err := NewGenerator(c).Validate(); err != nil {
		t.Fatalf("two sites with the same failure model must collapse to one: %v", err)
	}
	if !c.GuestFailsOpen() {
		t.Error("two agreeing fail-open sites did not resolve to fail-open")
	}
}

// The grace attributes are asked of `aaa` with the interval that will actually be
// emitted, so the generated reply cannot state two different intervals.
func TestGraceInterimMatchesTheConfiguredInterval(t *testing.T) {
	c := failureConfig(t, advice(t, "s", aaa.FailOpen, 600, 30))
	c.DefaultInterimIntervalSeconds = 900
	site := generateDefaultSite(t, c)
	if !strings.Contains(site, `Acct-Interim-Interval := "900"`) {
		t.Errorf("the grace block does not carry the generator's own interim interval; "+
			"the reply would state one value and then overwrite it with another:\n%s", site)
	}
}

func generateDefaultSite(t *testing.T, c Config) string {
	t.Helper()
	dir := t.TempDir()
	if err := NewGenerator(c).Generate(dir); err != nil {
		t.Fatalf("generating: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "sites-available", "default"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func stripComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
