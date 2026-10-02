package memory

import (
	"crypto/rand"
	"encoding/base32"
	"math/big"
	"strconv"
	"strings"
)

// voucherAlphabet has no O, no I, no L, no 0 and no 1.
//
// A voucher is read off a printed slip by somebody standing at a counter and
// typed into a phone, often in a second language. The characters that are
// removed are the ones two people disagree about.
const voucherAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

const voucherLength = 12

// newVoucherCode draws from crypto/rand, never math/rand.
//
// A voucher is a bearer credential for network access, so a predictable sequence
// is a way to get online without one. `docs/rules/security.md` §9 names this
// directly.
func newVoucherCode() (string, error) {
	limit := big.NewInt(int64(len(voucherAlphabet)))
	var b strings.Builder
	for i := 0; i < voucherLength; i++ {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		b.WriteByte(voucherAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// normaliseCode is how a submitted code is compared: upper-cased, with anything
// outside the alphabet dropped. A code copied with dashes and one copied without
// are the same code.
func normaliseCode(v string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(v) {
		if strings.ContainsRune(voucherAlphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// newSecret mints a RADIUS shared secret. There is no default and no fallback.
func newSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

func itoa(i int) string { return strconv.Itoa(i) }
