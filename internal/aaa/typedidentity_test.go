package aaa

import (
	"context"
	"strconv"
	"testing"
)

// What a User-Name has to be, byte for byte, to name a voucher or an account.
//
//	untrimmed  if the subject were strings.TrimSpace'd, Go would trim Unicode
//	           White_Space: a voucher followed by NBSP, NEL (U+0085, a C1
//	           control) or U+2028 would be the voucher. C1 is in the
//	           control-character refusal for the same reason.
//	typed      if the voucher lookup were the subject exactly, the code as
//	           PRINTED (`XXXX-XXXX-XXXX`) or typed in lowercase -- what a
//	           MikroTik hotspot page sends -- would be refused over RADIUS while
//	           the portal accepted it.
//
// Literals throughout, for the same reason remediation_test.go gives: a red
// proof reverts the implementation and runs these same tests.

// code is a voucher in its stored form: twelve characters of the alphabet.
const code = "ABCDEFGHJKMN"

func typedWorld() *mockStore {
	return &mockStore{
		vouchers: map[string]*VoucherInfo{
			code:   {Code: code, State: "unused", DurationMinutes: 60},
			"GOOD": {Code: "GOOD", State: "unused", DurationMinutes: 60},
		},
		accounts: map[string]*GuestAccountInfo{
			"alice": {Username: "alice", Enabled: true, DurationMinutes: 60},
			// A stored name that itself carries C1 controls: the refusal is of
			// the character, so it holds even when the lookup would match
			// exactly -- which is what makes the C1 half observable on its own,
			// apart from the trimming.
			"bob\u0085":         {Username: "bob\u0085", Enabled: true, DurationMinutes: 60},
			"carol\u0080\u009f": {Username: "carol\u0080\u009f", Enabled: true, DurationMinutes: 60},
		},
	}
}

func authorizeName(t *testing.T, name string) *Decision {
	t.Helper()
	dec, err := NewEngine(typedWorld(), 300).Authorize(context.Background(), Request{UserName: name})
	if err != nil {
		// An error is the failure branch, and at a fail-open site the failure
		// branch is an accept. A name is always a decision.
		t.Fatalf("%s: an error, which reaches the failure branch: %v", strconv.QuoteToASCII(name), err)
	}
	return dec
}

// TestAUnicodeSpaceIsNotTrimmedIntoAnIdentity locks the untrimmed subject.
func TestAUnicodeSpaceIsNotTrimmedIntoAnIdentity(t *testing.T) {
	for _, name := range []string{
		// C1 controls: refused as characters no identity holds.
		"GOOD\u0085", // NEL, which TrimSpace trims
		"GOOD\u0080",
		"GOOD\u009f",
		"\u0085GOOD",
		code + "\u0085",
		"alice\u0085",
		"bob\u0085",         // stored exactly so
		"carol\u0080\u009f", // stored exactly so
		// Unicode spaces that are not controls: never trimmed away.
		"GOOD\u00a0", // NBSP
		"\u00a0GOOD",
		"GOOD\u2028", // LINE SEPARATOR
		"GOOD\u3000", // IDEOGRAPHIC SPACE
		code + "\u00a0",
		"ABCD\u00a0EFGH\u00a0JKMN",
		"alice\u00a0",
		// ASCII blanks around an identity that has no typed form.
		"GOOD ",
		" GOOD",
		"GOOD\t",
		"alice ",
		" alice",
	} {
		if dec := authorizeName(t, name); dec.Allow {
			t.Errorf("%s was authorized: it is not any identity this store holds", strconv.QuoteToASCII(name))
		}
	}

	// The controls: the same store authorizes the identities themselves, so every
	// refusal above is about the character and not about a store that refuses all.
	for _, name := range []string{"GOOD", code, "alice"} {
		if dec := authorizeName(t, name); !dec.Allow {
			t.Fatalf("%q itself was refused (%s), so no refusal above proves anything", name, dec.Reason)
		}
	}
}

// TestAVoucherTypedAsPrintedIsTheVoucher locks the typed voucher form.
func TestAVoucherTypedAsPrintedIsTheVoucher(t *testing.T) {
	for _, typed := range []string{
		"ABCD-EFGH-JKMN", // as printed on the slip
		"abcd-efgh-jkmn",
		"abcdefghjkmn",
		"AbCdEfGhJkMn",
		"ABCD EFGH JKMN",
		"ABCD-EFGH-JKMN ", // an ASCII blank is a separator in a code, as at the portal
	} {
		dec := authorizeName(t, typed)
		if !dec.Allow {
			t.Errorf("%q was refused (%s); it is the voucher %s as a guest types it", typed, dec.Reason, code)
			continue
		}
		if dec.ReplyAttributes[kindAttribute] != "voucher" {
			t.Errorf("%q was allowed as %q, want as the voucher", typed, dec.ReplyAttributes[kindAttribute])
		}
	}

	// Normalisation is the portal's rule and no wider: a character outside the
	// voucher alphabet is not dropped, a letter whose Unicode upper case is
	// ASCII is not folded, and eleven or thirteen characters are not twelve.
	for _, typed := range []string{
		"ABCD-EFGH-JKM0",    // 0 is not in the alphabet
		"ABCD-EFGH-JKMO",    // nor is O
		"ABCD_EFGH_JKMN",    // an underscore is not a separator
		"ABCD.EFGH.JKMN",    // nor a dot
		"ABCD-EFGH-JKM",     // eleven
		"ABCD-EFGH-JKMNP",   // thirteen
		"ABCDEFGHJKM\u0131", // \u0131, whose upper case is I
		"ABCDEFGHJKM\uff2e", // full-width N
	} {
		if dec := authorizeName(t, typed); dec.Allow {
			t.Errorf("%s was authorized as the voucher %s", strconv.QuoteToASCII(typed), code)
		}
	}
}

// TestAnAccountIsItsNameExactly: the typed form is a voucher's, never an
// account's. An account called `alice` is not `ALICE`, and a username that happens
// to be twelve alphabet letters is looked up as itself when no voucher has it.
func TestAnAccountIsItsNameExactly(t *testing.T) {
	st := typedWorld()
	st.accounts["WXYZ-WXYZ-WXYZ"] = &GuestAccountInfo{Username: "WXYZ-WXYZ-WXYZ", Enabled: true, DurationMinutes: 60}
	e := NewEngine(st, 300)
	for name, want := range map[string]bool{
		"alice":          true,
		"ALICE":          false,
		"WXYZ-WXYZ-WXYZ": true,
		"wxyz-wxyz-wxyz": false,
	} {
		dec, err := e.Authorize(context.Background(), Request{UserName: name})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if dec.Allow != want {
			t.Errorf("%q: allow = %v, want %v", name, dec.Allow, want)
		}
	}
}

func TestVoucherKey(t *testing.T) {
	for in, want := range map[string]string{
		"ABCD-EFGH-JKMN": code,
		"abcd efgh jkmn": code,
		"--abcdefghjkmn": code,
		"":               "",
		"GOOD":           "",
		"ABCD\u00a0EFGH": "",
	} {
		got, ok := voucherKey(in)
		if got != want || ok != (want != "") {
			t.Errorf("voucherKey(%s) = %q, %v; want %q", strconv.QuoteToASCII(in), got, ok, want)
		}
	}
}
