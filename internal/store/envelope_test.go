package store_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/kiennt048/foyer/internal/store"
)

func key(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestEnvelopeRoundTrip(t *testing.T) {
	kr, err := store.NewKeyring("k1:" + key(t))
	fatalf(t, err, "building a keyring")

	aad := store.SecretContext("shared_secrets", "secret", "sec-1")
	plaintext := []byte("a shared secret from crypto/rand")

	sealed, err := kr.Seal(plaintext, aad)
	fatalf(t, err, "sealing")
	if bytes.Contains(sealed, plaintext) {
		t.Fatal("the plaintext is present in the sealed blob")
	}

	opened, err := kr.Open(sealed, aad)
	fatalf(t, err, "opening")
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("round trip changed the value: %q", opened)
	}
}

// Two seals of one value must not produce one ciphertext.
//
// A deterministic envelope would let anyone with SELECT on the table see which
// controllers share a credential and which access points share a secret, without
// ever decrypting anything.
func TestEnvelopeIsNotDeterministic(t *testing.T) {
	kr, err := store.NewKeyring("k1:" + key(t))
	fatalf(t, err, "building a keyring")

	aad := store.SecretContext("shared_secrets", "secret", "sec-1")
	a, err := kr.Seal([]byte("same value"), aad)
	fatalf(t, err, "first seal")
	b, err := kr.Seal([]byte("same value"), aad)
	fatalf(t, err, "second seal")

	if bytes.Equal(a, b) {
		t.Fatal("sealing the same value twice produced the same blob")
	}
}

// The binding, which is the reason SecretContext exists.
//
// Without it, anyone with UPDATE on `controllers` could move a known-good credential
// onto a controller they registered themselves, or move a site's RADIUS secret onto
// a client they added -- no decryption required, just a copy from one row to
// another.
func TestSealedValueCannotMoveBetweenRows(t *testing.T) {
	kr, err := store.NewKeyring("k1:" + key(t))
	fatalf(t, err, "building a keyring")

	sealed, err := kr.Seal([]byte("the api key"), store.SecretContext("controllers", "credential", "ctl-1"))
	fatalf(t, err, "sealing for ctl-1")

	if _, err := kr.Open(sealed, store.SecretContext("controllers", "credential", "ctl-2")); err == nil {
		t.Fatal("a credential sealed for ctl-1 opened as ctl-2")
	}
	if _, err := kr.Open(sealed, store.SecretContext("shared_secrets", "secret", "ctl-1")); err == nil {
		t.Fatal("a credential sealed for one column opened as another")
	}
}

func TestSealedValueDetectsTampering(t *testing.T) {
	kr, err := store.NewKeyring("k1:" + key(t))
	fatalf(t, err, "building a keyring")

	aad := store.SecretContext("shared_secrets", "secret", "sec-1")
	sealed, err := kr.Seal([]byte("the secret"), aad)
	fatalf(t, err, "sealing")

	for _, i := range []int{len(sealed) - 1, len(sealed) / 2, 8} {
		tampered := bytes.Clone(sealed)
		tampered[i] ^= 0x01
		if _, err := kr.Open(tampered, aad); err == nil {
			t.Fatalf("a blob with byte %d flipped still opened", i)
		}
	}
}

// Rotation: a retired key still reads rows sealed under it, and new rows use the
// new one. Without this the day a KEK is rotated is the day every stored secret
// becomes unreadable.
func TestKeyringReadsRowsSealedUnderARetiredKey(t *testing.T) {
	oldKey, newKey := key(t), key(t)

	previous, err := store.NewKeyring("2025-11:" + oldKey)
	fatalf(t, err, "building the previous keyring")

	aad := store.SecretContext("shared_secrets", "secret", "sec-1")
	sealedThen, err := previous.Seal([]byte("issued last year"), aad)
	fatalf(t, err, "sealing under the old key")

	rotated, err := store.NewKeyring("2026-08:" + newKey + ",2025-11:" + oldKey)
	fatalf(t, err, "building the rotated keyring")

	if rotated.ActiveKeyID() != "2026-08" {
		t.Fatalf("active key is %q, want the first entry", rotated.ActiveKeyID())
	}

	opened, err := rotated.Open(sealedThen, aad)
	fatalf(t, err, "opening a row sealed under the retired key")
	if string(opened) != "issued last year" {
		t.Fatalf("got %q", opened)
	}

	// A keyring that has dropped the old key must fail loudly and name the key it
	// is missing, rather than returning a decryption error nobody can act on.
	onlyNew, err := store.NewKeyring("2026-08:" + newKey)
	fatalf(t, err, "building a keyring without the retired key")
	if _, err := onlyNew.Open(sealedThen, aad); err == nil {
		t.Fatal("a keyring missing the sealing key still opened the blob")
	}
}

// No KEK is not silently no encryption.
func TestNoKeyringRefusesRatherThanPassingThrough(t *testing.T) {
	var kr *store.Keyring
	if _, err := kr.Seal([]byte("x"), nil); !errors.Is(err, store.ErrNoKeyring) {
		t.Fatalf("sealing with no keyring returned %v, want ErrNoKeyring", err)
	}
	if _, err := kr.Open([]byte("x"), nil); !errors.Is(err, store.ErrNoKeyring) {
		t.Fatalf("opening with no keyring returned %v, want ErrNoKeyring", err)
	}
}

func TestKeyringRejectsShortKeys(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := store.NewKeyring("k1:" + short); err == nil {
		t.Fatal("a 16-byte key was accepted; AES-256 needs 32")
	}
	if _, err := store.NewKeyring("k1:not base64 at all"); err == nil {
		t.Fatal("a non-base64 key was accepted")
	}
	if _, err := store.NewKeyring(""); err == nil {
		t.Fatal("an empty keyring specification was accepted")
	}
}
