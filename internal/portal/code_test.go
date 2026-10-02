package portal

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/identity"
)

// The voucher alphabet exists in three places, and this is what stops them
// drifting.
//
// `web/contract/src/index.ts` defines it for the admin console and the portal's
// client-side shape check; `internal/identity` draws from it when a batch is
// generated; this package compares against it when a guest submits one. Three
// copies of an alphabet is how a code the admin **printed on a receipt** becomes
// a code the portal **rejects**, and the guest is standing at a counter being
// told their voucher is wrong.
//
// The contract's copy is read at test time rather than transcribed, for the same
// reason the message catalogue is: a transcription is a fourth copy.

func contractAlphabet(t *testing.T) (alphabet string, length string) {
	t.Helper()
	body, err := os.ReadFile(filepath.FromSlash("../../web/contract/src/index.ts"))
	if err != nil {
		t.Fatalf("reading the contract's voucher constants: %v", err)
	}
	a := regexp.MustCompile(`VOUCHER_ALPHABET\s*=\s*"([A-Z0-9]+)"`).FindSubmatch(body)
	l := regexp.MustCompile(`VOUCHER_LENGTH\s*=\s*(\d+)`).FindSubmatch(body)
	if a == nil || l == nil {
		t.Fatal("did not find VOUCHER_ALPHABET and VOUCHER_LENGTH in web/contract/src/index.ts — " +
			"if they moved, this derivation moves with them rather than quietly matching nothing")
	}
	return string(a[1]), string(l[1])
}

// TestTheVoucherAlphabetIsOneAlphabet.
//
// Compared as **sets**, not as strings. The three spell it in two different
// orders, and the order is irrelevant to every use — what matters is which
// characters a generator may draw and which a comparison will accept. Requiring
// the strings to match would fail on a difference that cannot affect anybody, and
// a test that fails for a harmless reason is a test somebody edits.
func TestTheVoucherAlphabetIsOneAlphabet(t *testing.T) {
	fromContract, fromLength := contractAlphabet(t)

	if got := sortRunes(VoucherAlphabet); got != sortRunes(fromContract) {
		t.Errorf("this package's alphabet and the contract's differ:\n  here: %s\ncontract: %s",
			got, sortRunes(fromContract))
	}
	// The generator's, which decides what is printed.
	if got := sortRunes(identity.UnambiguousCharacterSet); got != sortRunes(fromContract) {
		t.Errorf("internal/identity generates from a different alphabet than the contract declares:\n"+
			"  identity: %s\n  contract: %s", got, sortRunes(fromContract))
	}
	if fromLength != "12" || VoucherLength != 12 {
		t.Errorf("the contract says a voucher is %s characters and this package says %d",
			fromLength, VoucherLength)
	}

	// And the excluded characters are excluded, which is the point of the set
	// rather than an accident of which twelve letters were typed. A voucher is
	// read off a thermal receipt and typed into a phone, often in a second
	// language, and these are the ones two people disagree about.
	for _, ambiguous := range []rune{'0', 'O', '1', 'I', 'L'} {
		if strings.ContainsRune(VoucherAlphabet, ambiguous) {
			t.Errorf("%q is in the voucher alphabet", ambiguous)
		}
	}
}

// TestNormaliseCodeAcceptsWhatAGuestActuallyTypes.
//
// Dropping everything outside `[A-Z0-9]` rather than everything outside the
// voucher alphabet is deliberate and matches `normaliseVoucherCode` in
// `web/contract`. A guest who typed `O` for `0` is told the code was not found,
// rather than having the character silently deleted and the remaining eleven
// compared against a twelve-character code — the first is a sentence they can act
// on and the second is "not found" for a reason nobody can see.
func TestNormaliseCodeAcceptsWhatAGuestActuallyTypes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ABCDEFGHJKMN", "ABCDEFGHJKMN"},
		{"abcd-efgh-jkmn", "ABCDEFGHJKMN"},
		{"  ABCD EFGH JKMN  ", "ABCDEFGHJKMN"},
		{"abcd_efgh.jkmn", "ABCDEFGHJKMN"},
		{"ABCDEFGHJKM0", "ABCDEFGHJKM0"}, // a zero survives, and is then not found
		{"", ""},
		{"!!!", ""},
	} {
		if got := NormaliseCode(tc.in); got != tc.want {
			t.Errorf("NormaliseCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// A zero typed for an `O` is shaped wrong rather than silently truncated, so
	// the guest is told the length is right and a character is not.
	if CodeShaped("ABCDEFGHJKM0") {
		t.Error("a code containing a character outside the alphabet passed the shape check")
	}
	if CodeShaped("ABCDEFGHJKM") {
		t.Error("an eleven-character code passed the shape check")
	}
	if !CodeShaped("abcd-efgh-jkmn") {
		t.Error("a code copied with separators failed the shape check")
	}
}

// TestSameCodeRefusesTheEmptyPair.
//
// `subtle.ConstantTimeCompare` on two empty strings returns 1, so a comparison
// that did not refuse the empty case explicitly would accept a submission of ""
// against a stored "". `sameToken` in `internal/api` carries the same guard for
// the same reason.
func TestSameCodeRefusesTheEmptyPair(t *testing.T) {
	if SameCode("", "") {
		t.Error("two empty codes compared equal")
	}
	if SameCode("ABCDEFGHJKMN", "") || SameCode("", "ABCDEFGHJKMN") {
		t.Error("an empty code matched a real one")
	}
	if !SameCode("ABCDEFGHJKMN", "ABCDEFGHJKMN") {
		t.Error("a code did not match itself")
	}
}

func sortRunes(s string) string {
	r := []rune(s)
	sort.Slice(r, func(i, j int) bool { return r[i] < r[j] })
	return string(r)
}
