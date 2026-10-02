package identity

import (
	"strings"
	"testing"
)

func TestASetupCodeIsEightyBitsAndRetypable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := NewSetupCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != SetupCodeLength+3 || strings.Count(code, "-") != 3 {
			t.Fatalf("%q is not four groups of four", code)
		}
		if seen[code] {
			t.Fatalf("%q came out twice in 200 draws of 80 bits", code)
		}
		seen[code] = true
		digest := SetupCodeDigest(code)
		// What an operator actually types: lower case, no dashes, spaces.
		retyped := strings.ToLower(strings.ReplaceAll(code, "-", " "))
		if !SameSetupCode(retyped, digest) {
			t.Fatalf("%q retyped as %q does not match", code, retyped)
		}
	}
}

func TestLookAlikesReadAsTheDigitsTheyResemble(t *testing.T) {
	d := SetupCodeDigest("0011-2222-3333-4444")
	if !SameSetupCode("OoIl-2222-3333-4444", d) {
		t.Fatal("O, o, I and l did not read as 0, 0, 1, 1")
	}
}

func TestAMalformedCodeMatchesNothing(t *testing.T) {
	for _, typed := range []string{"", "0000", "0000-0000-0000-000U", "0000-0000-0000-0000-0"} {
		if SetupCodeDigest(typed) != nil {
			t.Errorf("%q normalised to something", typed)
		}
		if SameSetupCode(typed, nil) {
			t.Errorf("%q matched an absent digest", typed)
		}
	}
}
