package portal

import (
	"crypto/subtle"
	"strings"
)

// VoucherAlphabet has no `O`, no `I`, no `L`, no `0` and no `1`.
//
// A voucher is read off a thermal receipt by somebody standing at a counter and
// typed into a phone, often in a second language. The characters removed are the
// ones two people disagree about. `web/contract/src/index.ts` defines the same
// set as `VOUCHER_ALPHABET`, `internal/identity` draws from it when a batch is
// generated, and `code_test.go` reads the contract at test time and requires all
// three to agree as sets. Three copies of an alphabet is how a code the admin
// **printed on a receipt** becomes a code the portal **rejects**.
const VoucherAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// VoucherLength is twelve characters, ungrouped on the wire. Grouping is display
// only: `ABCD-EFGH-JKMN` and `abcdefghjkmn` are the same code.
const VoucherLength = 12

// NormaliseCode accepts whatever the guest actually typed or pasted — any
// casing, any spacing, dashes or none — and returns the wire form.
//
// It drops anything outside `[A-Z0-9]` rather than anything outside the voucher
// alphabet, which is deliberate and matches `normaliseVoucherCode` in
// `web/contract`. A guest who typed `O` for `0` should be told the code was not
// found, not have the character silently deleted and the remaining eleven
// compared against a twelve-character code — the first is a sentence they can
// act on and the second is "not found" for a reason nobody can see.
func NormaliseCode(input string) string {
	var b strings.Builder
	b.Grow(len(input))
	for _, r := range strings.ToUpper(input) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CodeShaped reports whether a submission could be a voucher at all.
//
// Checked before any store call, so a submission that cannot be a voucher never
// reaches a lookup. That is one fewer opportunity for the lookup to leak timing
// and it is the difference between `error.shape` — "a voucher code is 12
// characters" — and `error.not-found`, which sends a guest to check twelve
// characters that were never the problem.
func CodeShaped(input string) bool {
	clean := NormaliseCode(input)
	if len(clean) != VoucherLength {
		return false
	}
	for _, r := range clean {
		if !strings.ContainsRune(VoucherAlphabet, r) {
			return false
		}
	}
	return true
}

// SameCode is constant-time.
//
// §9 asks for it on vouchers and OTPs, and the reason is that a voucher is a
// bearer credential for network access submitted by anybody within radio range.
//
// **It is honest about where it can and cannot be applied.** This function is
// what an in-memory store uses. A `WHERE code = $1` against an indexed column is
// *not* constant time and cannot be made so — the index is the thing that makes
// the portal answer in single-digit milliseconds — so on the SQL path the
// defence is the rate limit in front of it and the code's own entropy: twelve
// characters from a 31-character alphabet is a little over 59 bits, drawn from
// `crypto/rand`. That trade is written down in `CONTRACT.md` rather than left as
// an assumption somebody discovers.
func SameCode(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
