package raddb

import (
	"regexp"
	"strings"
	"testing"
)

// The generator's half of five hardening properties: an account allow still
// needs its password, every value in the Policy API question is encoded and an
// unreadable one is refused, the log carries no password, radiusd does not run as
// root, and the acct_unique fix. Each assertion reads the generated tree, which
// is what FreeRADIUS reads; what FreeRADIUS then DOES with it is proven against a
// real 3.2.10 in lab/policy, because no test here can run unlang.
//
// The wire strings are literals here rather than the package's or internal/aaa's
// constants, deliberately: a red proof reverts the implementation and runs these
// same tests, and a test that stops compiling proves less than one that fails. The aaa
// tests assert the same literals on the reply the Policy API serves, so the two
// halves agree through them.

// TestTheLogCarriesNoPassword: `auth_badpass = yes` writes every wrong
// password -- often a near-miss of the right one -- into stdout, the container log
// and every support bundle made from them.
func TestTheLogCarriesNoPassword(t *testing.T) {
	log := sectionOf(t, uncommented(readGenerated(t, generate(t, nil), "radiusd.conf")), "log {")
	for _, want := range []string{"auth_badpass = no", "auth_goodpass = no"} {
		if !strings.Contains(log, want) {
			t.Errorf("radiusd.conf's log section does not say %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "_badpass = yes") || strings.Contains(log, "_goodpass = yes") {
		t.Errorf("radiusd.conf logs submitted passwords:\n%s", log)
	}
}

// TestRadiusdDoesNotRunAsRoot is the generator half of the privilege drop. The lab asserts the uid of
// the running process; this asserts the tree asks for it.
func TestRadiusdDoesNotRunAsRoot(t *testing.T) {
	sec := sectionOf(t, uncommented(readGenerated(t, generate(t, nil), "radiusd.conf")), "security {")
	// freerad is the user both images create (lab/Dockerfile.freeradius and
	// deploy/Dockerfile.freeradius refuse to build without it).
	if !strings.Contains(sec, "user = freerad") || !strings.Contains(sec, "group = freerad") {
		t.Errorf("radiusd.conf does not switch to freerad:freerad:\n%s", sec)
	}
	if regexp.MustCompile(`(?m)^\s*(user|group)\s*=\s*(root|0)\s*$`).MatchString(sec) {
		t.Errorf("radiusd.conf runs radiusd as root:\n%s", sec)
	}
}

// valueRE finds every JSON string value in a rest `data` line that carries an
// expansion.
var valueRE = regexp.MustCompile(`:"(%\{[^"]*)"`)

// hexValueRE is a value that is an attribute's bytes as hex, and nothing else.
var hexValueRE = regexp.MustCompile(`^%\{hex:&[A-Za-z0-9-]+\}$`)

// TestEveryValueInThePolicyQuestionIsJSONEncoded is the generator half of strict
// request encoding.
//
// Measured on 3.2.10: a bare %{User-Name} spliced into the body escapes `"` and
// `\` (so the quote injection is not real) but prints a control byte as `\001`
// and a non-UTF-8 byte as `\377` -- not JSON. The Policy API answered 400, the
// virtual server sent that to the failure branch, and a fail-open site let the
// client on.
func TestEveryValueInThePolicyQuestionIsJSONEncoded(t *testing.T) {
	rest := uncommented(readGenerated(t, generate(t, eapConfig), "mods-available", "rest"))
	lines := 0
	for _, line := range strings.Split(rest, "\n") {
		if !strings.Contains(line, "data = '") {
			continue
		}
		lines++
		values := valueRE.FindAllStringSubmatch(line, -1)
		if len(values) < 7 {
			t.Fatalf("expected the eight-field question, found %d expanded values in:\n%s", len(values), line)
		}
		for _, v := range values {
			jsonquoted := strings.HasPrefix(v[1], "%{jsonquote:%{") && strings.HasSuffix(v[1], "}}")
			hexed := hexValueRE.MatchString(v[1])
			if !jsonquoted && !hexed {
				t.Errorf("a value is spliced into the JSON body without jsonquote or hex: %s", v[1])
			}
		}
		// The two identities are hex, never jsonquote: on the amd64
		// NetworkRADIUS build jsonquote turns every byte >= 0x80 into \u00XX,
		// so a non-ASCII name reached the Policy API as a different name
		// (measured).
		for _, key := range []string{`"user_name`, `"outer_user_name`} {
			if strings.Contains(line, key+`":"`) {
				t.Errorf("%s is sent jsonquoted, not as %s_hex:\n%s", key[1:], key[1:], line)
			}
			if !regexp.MustCompile(key + `_hex":"%\{hex:&[A-Za-z0-9-]+\}"`).MatchString(line) {
				t.Errorf("%s_hex is not %%{hex:&Attr}:\n%s", key[1:], line)
			}
		}
	}
	// authorize, authenticate and post-auth: three questions, and a regression
	// in any one is the same hole.
	if lines != 3 {
		t.Errorf("found %d data lines, want 3 (authorize, authenticate, post-auth)", lines)
	}
}

// TestAnUnreadableQuestionIsRefusedNotFailedOpen is the other half: rlm_rest
// answers `invalid` for the Policy API's 400, and `invalid` must reach the reject
// branch -- never the failure branch that a fail-open site turns into an accept.
func TestAnUnreadableQuestionIsRefusedNotFailedOpen(t *testing.T) {
	site := uncommented(generateDefaultSite(t, failureConfig(t, advice(t, "s", "fail-open", 600, 300))))
	authorize := sectionOf(t, site, "authorize {")
	if regexp.MustCompile(`elsif \([^)]*invalid`).MatchString(authorize) {
		t.Fatalf("`invalid` is routed into a branch of its own or the failure branch:\n%s", authorize)
	}
	if !strings.Contains(authorize, "elsif (fail) {") {
		t.Fatalf("the failure branch is not `fail` alone:\n%s", authorize)
	}
	// The rest call must still give `invalid` an action, or the section returns
	// on it before any branch runs.
	if !strings.Contains(authorize, "invalid = 1") {
		t.Error("rest's `invalid` has no action; unlang's default would end the section on it")
	}
}

// TestAnAccountAllowStillNeedsItsPassword is the generator half of the
// identity-kind reply.
//
// The allow branch was `Auth-Type := Accept` for every allow, so a guest
// account's username was enough. It must now accept on its own only for a
// voucher, send an account to PAP, and refuse everything else.
func TestAnAccountAllowStillNeedsItsPassword(t *testing.T) {
	site := uncommented(readGenerated(t, generate(t, nil), "sites-available", "default"))
	authorize := sectionOf(t, site, "authorize {")
	allow := sectionOf(t, authorize, "if (ok || updated) {")

	if !strings.Contains(allow, `if (&reply:Foyer-Identity-Kind == "voucher") {`) {
		t.Errorf("the allow branch does not condition its accept on the identity being a voucher:\n%s", allow)
	}
	voucher := sectionOf(t, allow, `if (&reply:Foyer-Identity-Kind == "voucher") {`)
	if strings.Count(allow, "Auth-Type := Accept") != 1 || !strings.Contains(voucher, "Auth-Type := Accept") {
		t.Errorf("an accept exists in the allow branch outside the voucher arm:\n%s", allow)
	}
	account := sectionOf(t, allow, `elsif ((&reply:Foyer-Identity-Kind == "account") && &User-Password) {`)
	if !strings.Contains(account, "Auth-Type := PAP") || !strings.Contains(account, "radius_pap_verifiers") {
		t.Errorf("the account arm does not verify the password against radius_pap_verifiers:\n%s", account)
	}
	if !strings.Contains(account, "Auth-Type := Reject") {
		t.Errorf("an account with no verifiable password is not refused:\n%s", account)
	}
	if !strings.Contains(allow, "&Foyer-Identity-Kind !* ANY") {
		t.Error("the kind is left in the reply list")
	}
	// PAP must actually be reachable from authenticate.
	if !strings.Contains(sectionOf(t, site, "authenticate {"), "Auth-Type PAP {") {
		t.Error("authenticate has no Auth-Type PAP, so the account arm has nowhere to go")
	}
}

// TestTheKindIsDefinedAsADictionaryAttributeNotAsConfiguration: the attribute
// must exist -- the unlang names it, and measured on 3.2.10 a tree without the
// definition does not start ("Unknown attribute") -- and radiusd.conf must not
// $INCLUDE the file that defines it, because an ATTRIBUTE line parsed as
// configuration stops the server too.
func TestTheKindIsDefinedAsADictionaryAttributeNotAsConfiguration(t *testing.T) {
	dir := generate(t, nil)
	dict := readGenerated(t, dir, "dictionary")
	m := regexp.MustCompile(`(?m)^ATTRIBUTE\s+Foyer-Identity-Kind\s+(\d+)\s+string\s*$`).FindStringSubmatch(dict)
	if m == nil {
		t.Fatalf("the dictionary does not define Foyer-Identity-Kind:\n%s", dict)
	}
	// 3000-4000 is the range FreeRADIUS never encodes into a packet.
	if m[1] < "3000" || m[1] > "3999" || len(m[1]) != 4 {
		t.Errorf("Foyer-Identity-Kind is %s, outside the server-internal 3000-3999 range", m[1])
	}
	if strings.Contains(uncommented(readGenerated(t, dir, "radiusd.conf")), "$INCLUDE dictionary") {
		t.Error("radiusd.conf $INCLUDEs the dictionary, which parses ATTRIBUTE lines as configuration")
	}
}

// TestARejectCarriesAReasonAndNothingElse: the Policy API's allow puts the plan's
// attributes into the reply before rlm_pap refuses a wrong password, and measured
// on 3.2.10 they rode out in the Access-Reject -- telling a hotspot login page
// that the username is a real account.
func TestARejectCarriesAReasonAndNothingElse(t *testing.T) {
	for _, name := range []string{"plain", "eap"} {
		t.Run(name, func(t *testing.T) {
			var mutate func(*Config)
			if name == "eap" {
				mutate = eapConfig
			}
			site := uncommented(readGenerated(t, generate(t, mutate), "sites-available", "default"))
			rej := sectionOf(t, site, "Post-Auth-Type REJECT {")
			wipe := strings.Index(rej, "&reply: !* ANY")
			if wipe < 0 {
				t.Fatalf("Post-Auth-Type REJECT does not empty the reply:\n%s", rej)
			}
			if sql := strings.Index(rej, "sql"); sql < 0 || sql > wipe {
				t.Error("the reply is emptied before radpostauth is written")
			}
			if eap := strings.Index(rej, "eap"); name == "eap" && (eap < 0 || eap < wipe) {
				t.Error("the reply is emptied after eap adds EAP-Failure, which would strip it")
			}
			if !strings.Contains(rej[wipe:], "&Reply-Message := &request:Tmp-String-5") {
				t.Error("the reject's reason is not put back")
			}
		})
	}
}

// TestNothingSitsBetweenRestAndTheTestOfItsRcode: an unlang rcode condition
// (`ok`, `updated`, `fail`) reads the LAST statement's rcode, and an `update` is a
// statement (noop). Putting the kind's removal between `rest` and
// `if (!ok && !updated)` turns every EAP-TLS accept into a reject -- which lab/eap
// on 3.2.10 shows and reading the template does not.
func TestNothingSitsBetweenRestAndTheTestOfItsRcode(t *testing.T) {
	site := uncommented(readGenerated(t, generate(t, eapConfig), "sites-available", "default"))
	for _, tc := range []struct{ section, test string }{
		{"authorize {", "if (ok || updated) {"},
		{"post-auth {", "if (!ok && !updated) {"},
	} {
		sec := sectionOf(t, site, tc.section)
		rest := sectionOf(t, sec, "rest {")
		after := sec[strings.Index(sec, rest)+len(rest):]
		i := strings.Index(after, tc.test)
		if i < 0 {
			t.Fatalf("%s has no %q after its rest call", tc.section, tc.test)
		}
		if between := strings.TrimSpace(after[:i]); between != "" {
			t.Errorf("%s: a statement sits between `rest` and %q, so the test reads its rcode instead:\n%s",
				tc.section, tc.test, between)
		}
	}
}

// TestAccountingSessionsGetAUniqueID is the acct_unique finding: without it
// Acct-Unique-Session-Id expands to ” and radacct.acctuniqueid (NOT NULL UNIQUE)
// holds one session.
func TestAccountingSessionsGetAUniqueID(t *testing.T) {
	site := uncommented(readGenerated(t, generate(t, nil), "sites-available", "default"))
	pre := sectionOf(t, site, "preacct {")
	if !strings.Contains(pre, `&Acct-Unique-Session-Id := "%{md5:`) || !strings.Contains(pre, "%{Acct-Session-ID}") {
		t.Errorf("preacct does not compute Acct-Unique-Session-Id:\n%s", pre)
	}
}
