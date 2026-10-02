package memory

import (
	"slices"
	"testing"

	"github.com/kiennt048/foyer/internal/store"
)

// TestPhoneFormsAreTheStores holds this world's phone parse to store.PhoneSubject,
// the one the product erases with: this world does not import the store, so the
// rule is written twice and this is what keeps the two copies one rule.
func TestPhoneFormsAreTheStores(t *testing.T) {
	for _, raw := range []string{"0912 345 678", "+84 912 345 678", "84912345678", "(091) 234-5678",
		"+44 7700 900123", "1234567", "0912345678901234", "guest@example.com", "09+12345678", ""} {
		digits, spellings, ok := phoneForms(raw)
		s, err := store.PhoneSubject(raw)
		if ok != (err == nil) {
			t.Errorf("%q: this world accepts=%v, the store accepts=%v", raw, ok, err == nil)
			continue
		}
		if ok && (!slices.Equal(digits, s.PhoneDigits) || !slices.Equal(spellings, s.PhoneSpellings)) {
			t.Errorf("%q: this world %v %v, the store %v %v", raw, digits, spellings, s.PhoneDigits, s.PhoneSpellings)
		}
	}
}
