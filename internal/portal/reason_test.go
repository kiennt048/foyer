package portal

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The vocabulary the server sends and the vocabulary the portal can render are
// two files in two languages, and nothing but this connects them.
//
// A `303` carries `?e=<reason>`; `web/portal` looks up `error.<reason>` in its
// own catalogue and falls back to `error.generic` when it misses. That fallback
// is what makes the drift silent: a reason with no copy does not crash, it tells
// a guest standing in a café "Something went wrong. Try again." about a voucher
// that was cancelled an hour ago, and every test on both sides stays green.
//
// So the catalogue is read, here, at test time. It couples a Go test to a
// TypeScript file in the same repository, which is the point: they are two halves
// of one contract and the only alternative is a comment asking somebody to
// remember.

const catalogue = "../../web/portal/src/i18n/en.ts"

var errorKey = regexp.MustCompile(`"error\.([a-z-]+)"\s*:`)

func catalogueReasons(t *testing.T) map[string]bool {
	t.Helper()
	body, err := os.ReadFile(filepath.FromSlash(catalogue))
	if err != nil {
		// Not skipped. A missing catalogue means either the frontend moved or
		// this test is being run somewhere it cannot see the repository, and
		// both are things somebody has to know about — `docs/rules/testing.md`
		// forbids marking a test skipped to make CI green.
		t.Fatalf("reading the portal's message catalogue: %v\n"+
			"It is what says which failures the portal can render. If it moved, this path moves with it.", err)
	}
	found := map[string]bool{}
	for _, m := range errorKey.FindAllStringSubmatch(string(body), -1) {
		found[m[1]] = true
	}
	if len(found) == 0 {
		t.Fatal("read no error.* keys out of the catalogue — the derivation is vacuous, " +
			"which is worse than a wrong answer because it passes")
	}
	return found
}

// TestEveryReasonHasCopy — the direction that hurts a guest.
func TestEveryReasonHasCopy(t *testing.T) {
	have := catalogueReasons(t)
	for _, r := range Reasons() {
		if !have[string(r)] {
			t.Errorf("the server can send ?e=%s and the portal has no error.%s — "+
				"a guest gets error.generic for a failure the venue can explain", r, r)
		}
	}
}

// TestEveryCopyHasAReason — the other direction, and it is the contract's own
// rule about `ProblemType` applied to this vocabulary.
//
// A message nothing can produce is a catalogue entry that can never be shown to
// be right. It is also a translator's time spent on a sentence no guest will read
// and, worse, a sentence a reviewer will assume is reachable.
func TestEveryCopyHasAReason(t *testing.T) {
	known := map[string]bool{}
	for _, r := range Reasons() {
		known[string(r)] = true
	}
	var orphans []string
	for k := range catalogueReasons(t) {
		if !known[k] {
			orphans = append(orphans, k)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("the portal has copy for failures nothing can send: %s", strings.Join(orphans, ", "))
	}
}

// TestLanguageIsAnAllowlist.
//
// The value lands in a `Location` header. Escaping is not the defence here and
// never was: two known strings cannot carry a traversal, a newline or a scheme,
// and an escape can always be got wrong by somebody adding a third language.
func TestLanguageIsAnAllowlist(t *testing.T) {
	for _, bad := range []string{
		"", "de", "EN",
		"../..", "..%2f..%2fevil.example", "/etc/passwd",
		"en\r\nSet-Cookie: a=b",
		"en\nLocation: https://evil.example",
		"https://evil.example", "//evil.example",
		"en/../../vi", "en ", " en",
		strings.Repeat("e", 4096),
	} {
		if got := Language(bad); got != "en" && got != "vi" {
			t.Errorf("Language(%q) = %q — not one of the two the portal ships", bad, got)
		}
	}
	if Language("vi") != "vi" {
		t.Error("Language dropped a language the portal actually ships")
	}
}

// TestFailureURLCarriesOnlyWhatItMayCarry.
//
// The voucher code comes back so a guest who mistyped one of twelve characters
// does not retype all twelve — there is no client state to hold it and the flow
// must survive JavaScript being off. It comes back **normalised**, which is what
// makes that safe rather than merely convenient: a hand-written link cannot use
// this parameter to put arbitrary text on a page a stranger is reading.
func TestFailureURLCarriesOnlyWhatItMayCarry(t *testing.T) {
	for _, submitted := range []string{
		`<script>alert(1)</script>`,
		`" onmouseover="alert(1)`,
		`ABCD-EFGH-JKMN`,
		`../../etc/passwd`,
		strings.Repeat("A", 400),
		"\r\nSet-Cookie: a=b",
	} {
		got := Failure(PageVoucher, "en", ReasonNotFound, submitted)
		if strings.ContainsAny(got, "<>\"' \r\n") {
			t.Errorf("Failure(%q) produced %q — a URL that carries markup or a header break", submitted, got)
		}
		if !strings.HasPrefix(got, "/en/voucher/?") {
			t.Errorf("Failure(%q) = %q — left the portal", submitted, got)
		}
	}

	// The useful case still works: a guest who typed the code with dashes gets
	// it back in the wire form, which is what the field expects.
	if got := Failure(PageVoucher, "en", ReasonNotFound, "abcd-efgh-jkmn"); !strings.Contains(got, "code=ABCDEFGHJKMN") {
		t.Errorf("the submitted code did not come back normalised: %q", got)
	}
	// And a code that normalises to nothing adds no parameter at all rather than
	// an empty one the portal would render as an empty field.
	if got := Failure(PageVoucher, "en", ReasonNotFound, "!!!"); strings.Contains(got, "code=") {
		t.Errorf("an empty echo still produced a parameter: %q", got)
	}
}

// TestNothingSubmittedToTheAccountFormIsEchoed.
//
// The voucher path echoes because a guest cannot reasonably retype twelve random
// characters. A username is not in that position and a password never is: a
// username in a redirect is a username in every proxy log between the phone and
// the portal, which is the same rule `AuthAttempt` carries.
func TestNothingSubmittedToTheAccountFormIsEchoed(t *testing.T) {
	got := Failure(PageAccount, "en", ReasonBadCredentials, "kim@example.test")
	if strings.Contains(got, "kim") || strings.Contains(got, "code=") {
		t.Errorf("the account failure URL carried a submitted value: %q", got)
	}
}
