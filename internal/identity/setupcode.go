package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"strings"
)

// The console's first-access setup code (CONTRACT.md invariant 14).
//
// Foyer ships no credential. A fresh install has no operator; the installer opens
// a one-time setup and prints one of these, and whoever types it into the console
// creates the first administrator. It is Jenkins' `initialAdminPassword` shape
// rather than authentik's or UniFi's first-come wizard, because this console is
// published on every interface and a first-come setup belongs to whoever on the
// venue LAN opens the page first.

// setupAlphabet is Crockford's base32: no I, L, O or U, so a code read off a
// terminal cannot be mistyped between look-alike characters.
const setupAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// SetupCodeLength is the number of characters, five bits each: 80 bits.
const SetupCodeLength = 16

// NewSetupCode returns a fresh code for display, grouped `XXXX-XXXX-XXXX-XXXX`.
//
// 256 is a multiple of 32, so a byte masked to five bits is uniform; no rejection
// sampling is needed.
func NewSetupCode() (string, error) {
	raw := make([]byte, SetupCodeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, v := range raw {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(setupAlphabet[v&31])
	}
	return b.String(), nil
}

// NormaliseSetupCode is what a typed code is compared as: upper case, no spaces or
// dashes, and the three look-alikes Crockford maps read as the digits they resemble.
// Anything that is not in the alphabet afterwards makes the whole code invalid ("").
func NormaliseSetupCode(typed string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(typed) {
		switch r {
		case ' ', '-', '\t':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		if !strings.ContainsRune(setupAlphabet, r) {
			return ""
		}
		b.WriteRune(r)
	}
	if b.Len() != SetupCodeLength {
		return ""
	}
	return b.String()
}

// SetupCodeDigest is what is stored: SHA-256 of the normalised code. Nil for a
// code that does not normalise, so an invalid code can never match a stored digest.
func SetupCodeDigest(typed string) []byte {
	n := NormaliseSetupCode(typed)
	if n == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(n))
	return sum[:]
}

// SameSetupCode compares a typed code against a stored digest in constant time.
func SameSetupCode(typed string, stored []byte) bool {
	got := SetupCodeDigest(typed)
	if got == nil || len(stored) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare(got, stored) == 1
}
