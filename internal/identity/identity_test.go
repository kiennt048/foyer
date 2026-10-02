package identity

import (
	"strings"
	"testing"
)

func TestArgon2id_HashAndVerify(t *testing.T) {
	password := "operator_SuperSecret_2026!"

	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	if !strings.HasPrefix(encoded, "$argon2id$v=") {
		t.Fatalf("expected argon2id prefix, got: %s", encoded)
	}

	// Verify correct password
	match, err := VerifyPassword(password, encoded)
	if err != nil {
		t.Fatalf("VerifyPassword failed: %v", err)
	}
	if !match {
		t.Errorf("expected password to match")
	}

	// Verify incorrect password
	matchWrong, err := VerifyPassword("wrong_password", encoded)
	if err != nil {
		t.Fatalf("VerifyPassword with wrong password returned error: %v", err)
	}
	if matchWrong {
		t.Errorf("expected wrong password to not match")
	}
}

func TestGenerateVoucherCode_UnambiguousCharacters(t *testing.T) {
	for i := 0; i < 100; i++ {
		code, err := GenerateVoucherCode(8)
		if err != nil {
			t.Fatalf("GenerateVoucherCode failed: %v", err)
		}
		if len(code) != 8 {
			t.Errorf("expected length 8, got %d", len(code))
		}

		for _, ch := range []string{"0", "O", "1", "I", "L"} {
			if strings.Contains(code, ch) {
				t.Errorf("voucher code %s contains ambiguous character %s", code, ch)
			}
		}
	}
}

func TestGenerateBatchCodes_Uniqueness(t *testing.T) {
	codes, err := GenerateBatchCodes(100, 8)
	if err != nil {
		t.Fatalf("GenerateBatchCodes failed: %v", err)
	}

	if len(codes) != 100 {
		t.Fatalf("expected 100 codes, got %d", len(codes))
	}

	seen := make(map[string]bool)
	for _, c := range codes {
		if seen[c] {
			t.Errorf("duplicate voucher code generated: %s", c)
		}
		seen[c] = true
	}
}

func TestSessionToken_GenerateAndHash(t *testing.T) {
	token, tokenHash, err := GenerateSessionToken()
	if err != nil {
		t.Fatalf("GenerateSessionToken failed: %v", err)
	}

	if len(token) == 0 || len(tokenHash) == 0 {
		t.Fatalf("expected non-empty token and hash")
	}

	expectedHash := HashSessionToken(token)
	if expectedHash != tokenHash {
		t.Errorf("expected hash %s, got %s", expectedHash, tokenHash)
	}
}
