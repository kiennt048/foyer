package otp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"strings"
)

// CodeLength is six digits: the length every OTP channel a guest has seen uses
// (bank, Zalo, Grab), and the minimum NIST SP 800-63B §5.1.3 sets for an
// out-of-band secret. It is ~19.9 bits, which is exactly why guessing is bounded by
// attempts and lifetime rather than by the code.
const CodeLength = 6

// NewCode draws six decimal digits uniformly from `crypto/rand`.
//
// `rand.Int` rather than a byte modulo 10: `b % 10` over a byte is biased towards
// 0–5 (256 is not a multiple of 10), and a biased code is a smaller code.
func NewCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	s := n.String()
	return strings.Repeat("0", CodeLength-len(s)) + s, nil
}

// NormaliseCode strips what a guest may type around six digits, and refuses
// anything else. The empty string means *not a code*.
func NormaliseCode(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == ' ':
		default:
			return ""
		}
	}
	if b.Len() != CodeLength {
		return ""
	}
	return b.String()
}

// ── how a code is kept ───────────────────────────────────────────────────────
//
// **A keyed hash, not argon2, and not the code.** The choice is forced by the size
// of the thing being protected:
//
//   - A six-digit code is a million values. Any *unkeyed* hash of it — sha256,
//     bcrypt, argon2id at the 64 MiB Foyer uses for passwords — is recovered from a
//     database dump by trying all million: even at one argon2 evaluation per
//     second that is under twelve days on one core, and it parallelises perfectly.
//     A slow hash buys a constant factor against an exhaustive search the
//     attacker can always finish.
//   - A **keyed** hash is not searchable at all without the key. So the protection
//     is where the key lives, and the key is derived (HKDF-SHA256) from the
//     deployment's key-encryption key — `FOYER_KEK`, the key that already unseals
//     every enveloped column and that is by rule never in the database. A dump
//     of `phone_codes` is a table of HMACs nobody can invert.
//   - And it must be **cheap**. An unbounded argon2 was measured to cost this
//     process (64 MiB per check, OOM at ~5 in
//     parallel); verifying a five-minute code with it would hand anyone in radio
//     range the same lever. HMAC-SHA256 is microseconds.
//
// The MAC binds the code to **the row and the browser**: its input is the code id,
// the portal session's token hash and the code, so a MAC copied onto another row
// or another session does not verify. The comparison is `hmac.Equal`, constant
// time (§9).

const macLabel = "foyer phone-code v1"

// MAC computes the stored form of one code.
func MAC(key []byte, codeID, sessionTokenHash, code string) []byte {
	m := hmac.New(sha256.New, key)
	for _, part := range []string{macLabel, codeID, sessionTokenHash, code} {
		m.Write([]byte(part))
		m.Write([]byte{0})
	}
	return m.Sum(nil)
}

// SameMAC is the constant-time comparison.
func SameMAC(a, b []byte) bool { return hmac.Equal(a, b) }

// newID is a code's row id, `otp_` and 32 hex digits from `crypto/rand`. It is also
// the provider's `RequestId`, which de-duplicates a retried send.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "otp_" + hex.EncodeToString(b[:]), nil
}
