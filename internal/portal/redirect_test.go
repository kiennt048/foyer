package portal

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The open-redirect validator, against the payloads that actually get used.
//
// This is the one place in the portal where the threat is not theoretical, and
// the reason is in the rule rather than in the code: the victim is on an
// untrusted network, *expects* to be redirected, and has just typed credentials.
// A redirect to a look-alike host is suspicious anywhere else and is the
// advertised behaviour of the product here.

func TestSafeReturnURLRefusesEveryShapeThatIsNotAnHTTPDestination(t *testing.T) {
	for _, raw := range []string{
		// Schemes. The first two are what everybody tests for.
		"javascript:alert(1)",
		"data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
		"file:///etc/passwd",
		"vbscript:msgbox(1)",
		"JaVaScRiPt:alert(1)",
		" javascript:alert(1)",
		"intent://evil.example#Intent;scheme=http;end",
		"market://details?id=x",
		"mailto:someone@evil.example",

		// Protocol-relative. Not a scheme at all — it inherits the portal's, so a
		// check that looked only at `u.Scheme` passes it straight through.
		"//evil.example",
		"//evil.example/path",
		"\\\\evil.example",

		// Userinfo. This *is* http, with a host of `evil.example`, and the half a
		// guest reads is the half that is not the host.
		"http://portal.example.test@evil.example",
		"https://portal.example.test:pw@evil.example/login",
		"https://portal.example.test%40evil.example@evil.example",

		// Header and renderer breaks.
		"http://evil.example/\r\nSet-Cookie: a=b",
		"http://evil.example/\nLocation: https://evil.example",
		"http://evil.example/\x00",
		"http://evil exa mple/",

		// Not a URL at all.
		"",
		"   ",
		"://",
		"http://",
		"https://",
	} {
		if got := SafeReturnURL(raw); got != "" {
			t.Errorf("SafeReturnURL(%q) = %q — it should vouch for nothing it cannot parse as an http destination", raw, got)
		}
	}
}

func TestSafeReturnURLKeepsARealDestination(t *testing.T) {
	for _, raw := range []string{
		"http://example.test/",
		"https://example.test/news/story?id=4#top",
		"https://example.test:8443/path",
		"https://xn--80ak6aa92e.example/",
	} {
		if got := SafeReturnURL(raw); got == "" {
			t.Errorf("SafeReturnURL(%q) refused a destination a NAS really sends", raw)
		}
	}
}

// TestSafeReturnURLAgreesWithTheContractsPattern.
//
// `PortalStatus.originalUrl` carries a `pattern` in `openapi.yaml`, and the
// contract says why the duplication is deliberate: "a regex is a shape test and
// `URL` is a parser, and the browser uses a parser". Neither replaces the other —
// but they must not *disagree*, because a value this function vouches for and the
// schema rejects is a `500` from runtime contract validation on the one screen a
// guest sees after typing a credential.
//
// The pattern is read out of the contract rather than copied here, for the same
// reason the message catalogue is read rather than copied.
func TestSafeReturnURLAgreesWithTheContractsPattern(t *testing.T) {
	body, err := os.ReadFile(filepath.FromSlash("../../web/contract/openapi.yaml"))
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	// The pattern as the document spells it, in the `originalUrl` block.
	m := regexp.MustCompile(`pattern:\s*"(\^https\?[^"]+)"`).FindSubmatch(body)
	if m == nil {
		t.Fatal("did not find PortalStatus.originalUrl's pattern in the contract — " +
			"if it moved or changed shape, this derivation has to move with it rather than quietly matching nothing")
	}
	// YAML doubles the backslashes; Go's regexp wants them single.
	expr := strings.ReplaceAll(string(m[1]), `\\`, `\`)
	re, err := regexp.Compile(expr)
	if err != nil {
		t.Fatalf("the contract's pattern does not compile as a Go regexp: %v", err)
	}

	// The compiled pattern has to be capable of refusing something, or the
	// comparison below is two functions agreeing that everything is fine.
	if re.MatchString("javascript:alert(1)") || !re.MatchString("https://example.test/") {
		t.Fatalf("the pattern read out of the contract does not discriminate: %q", expr)
	}

	for _, raw := range []string{
		"javascript:alert(1)", "//evil.example",
		"http://portal.example.test@evil.example",
		"http://example.test/", "https://example.test/a?b=c#d",
		"data:text/html,x", "http://evil.example/\r\nSet-Cookie: a=b",
	} {
		vouched := SafeReturnURL(raw) != ""
		matched := re.MatchString(raw)
		if vouched && !matched {
			t.Errorf("SafeReturnURL vouched for %q and the contract's own pattern rejects it — "+
				"the server would answer a body its schema refuses", raw)
		}
	}
}
