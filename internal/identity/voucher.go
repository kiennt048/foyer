package identity

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

// UnambiguousCharacterSet excludes visually ambiguous characters (0, O, 1, I, L).
const UnambiguousCharacterSet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// GenerateVoucherCode generates a cryptographically random voucher code of the specified length.
func GenerateVoucherCode(length int) (string, error) {
	if length <= 0 {
		length = 8
	}

	charsetLen := big.NewInt(int64(len(UnambiguousCharacterSet)))
	var b strings.Builder
	b.Grow(length)

	for i := 0; i < length; i++ {
		idx, err := rand.Int(rand.Reader, charsetLen)
		if err != nil {
			return "", fmt.Errorf("failed to generate random index: %w", err)
		}
		b.WriteByte(UnambiguousCharacterSet[idx.Int64()])
	}

	return b.String(), nil
}

// GenerateBatchCodes generates n unique voucher codes using crypto/rand.
func GenerateBatchCodes(count, length int) ([]string, error) {
	if count <= 0 {
		return nil, nil
	}

	seen := make(map[string]struct{}, count)
	codes := make([]string, 0, count)

	for len(codes) < count {
		code, err := GenerateVoucherCode(length)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[code]; !exists {
			seen[code] = struct{}{}
			codes = append(codes, code)
		}
	}

	return codes, nil
}
