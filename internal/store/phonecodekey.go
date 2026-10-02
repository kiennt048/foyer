package store

import (
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
)

// The key phone sign-in MACs its codes under, derived from FOYER_KEK.
//
// `internal/identity/otp` stores a six-digit code as HMAC-SHA256 under a key that
// is **not in the database**, because a million possible codes is a search any
// unkeyed hash loses (the reasoning is in `otp/code.go`). The KEK is already the
// one secret this deployment keeps out of the database by rule, so the key is
// derived from it rather than being a second secret to provision, rotate and
// forget.
//
// HKDF-SHA256 with a purpose label, so the subkey is independent of the KEK's use
// as an AES-GCM wrapping key: knowing one reveals nothing about the other, and a
// future second purpose takes a second label rather than the same bytes. Derived
// once, when the keyring is parsed; the raw KEK is not retained.
//
// Each keyring entry carries its own subkey and the code row records which entry
// made it, so a code issued five minutes before a KEK rotation still verifies
// afterwards against the key it was made with.

const phoneCodeInfo = "foyer/phone-code-mac/v1"

func derivePhoneCodeKey(kek []byte) ([]byte, error) {
	k, err := hkdf.Key(sha256.New, kek, nil, phoneCodeInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("deriving the phone-code key: %w", err)
	}
	return k, nil
}

// PhoneCodeKey returns the active entry's subkey and its id.
func (k *Keyring) PhoneCodeKey() (string, []byte, error) {
	if k == nil {
		return "", nil, ErrNoKeyring
	}
	return k.active.id, append([]byte(nil), k.active.phoneCode...), nil
}

// PhoneCodeKeyByID returns the subkey of a named entry, for a code made before the
// active key changed.
func (k *Keyring) PhoneCodeKeyByID(id string) ([]byte, error) {
	if k == nil {
		return nil, ErrNoKeyring
	}
	e, ok := k.byID[id]
	if !ok {
		return nil, fmt.Errorf("store: no keyring entry %q", id)
	}
	return append([]byte(nil), e.phoneCode...), nil
}
