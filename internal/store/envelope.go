package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Envelope encryption for the three column families that hold recoverable secrets:
// RADIUS shared secrets, controller credentials and NT hashes.
//
// Why an envelope rather than encrypting each value directly under the KEK:
//
//   - **Rotation is a re-wrap, not a re-encrypt.** Changing the key-encryption key
//     rewrites 48 bytes per row instead of every ciphertext, so rotating is cheap
//     enough to actually happen.
//   - **The KEK is used once per row**, not once per byte of plaintext, which keeps
//     it far from any nonce-reuse limit.
//   - **Old keys stay decryptable.** The blob names the key that wrapped it, so a
//     keyring holding the previous KEK reads old rows while new rows use the new
//     one.
//
// And why the associated data is not optional: every Seal is bound to the table,
// column and row it belongs to, so a ciphertext lifted out of one row and pasted
// into another fails to open. Without that, an attacker with UPDATE on the
// controllers table could move a known-good credential onto a controller they
// control, or move a site's shared secret onto a client they added.
//
// FOYER_KEK is read from the environment and never from the database. A key stored
// beside the data it protects is an encoding, not encryption.

// ErrNoKeyring is returned when a sealed column is read or written by a process
// with no KEK configured. It is deliberately not a silent pass-through: handing
// back ciphertext as if it were a secret is how a shared secret ends up in
// clients.conf as forty bytes of base64.
var ErrNoKeyring = errors.New("store: no key-encryption key configured (set FOYER_KEK)")

// ErrSecretUnset is returned when a column that may legitimately hold no secret
// holds none -- a controller registered before its API key was pasted in.
var ErrSecretUnset = errors.New("store: no secret stored")

const (
	envelopeVersion = 1
	dekSize         = 32 // AES-256
)

var envelopeMagic = [3]byte{'F', 'K', 'v'}

type kekEntry struct {
	id   string
	aead cipher.AEAD
	// phoneCode is the HKDF subkey phone sign-in MACs its codes under. Derived
	// once here so the KEK itself is never kept outside its AEAD — see
	// phonecodekey.go.
	phoneCode []byte
	// auditPseudonym is the HKDF subkey an erasure's audit pseudonyms are keyed
	// under — see auditpseudonym.go.
	auditPseudonym []byte
}

// Keyring holds the active key-encryption key and any retired ones still needed to
// read rows sealed before the last rotation.
type Keyring struct {
	active  kekEntry
	byID    map[string]kekEntry
	ordered []string
}

// NewKeyring parses a keyring specification.
//
// The format is `id:base64key`, comma-separated, **active key first**:
//
//	FOYER_KEK=2026-08:aBc...=,2025-11:XyZ...=
//
// A bare base64 key with no id is accepted and given the id `0`, because the first
// deployment to set this variable should not have to learn a format to do it. The
// id exists so a later rotation does not have to guess which key sealed which row.
func NewKeyring(spec string) (*Keyring, error) {
	parts := strings.Split(spec, ",")
	kr := &Keyring{byID: map[string]kekEntry{}}

	for i, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id, material := "0", raw
		if idx := strings.Index(raw, ":"); idx >= 0 {
			id, material = strings.TrimSpace(raw[:idx]), strings.TrimSpace(raw[idx+1:])
		}
		if id == "" {
			return nil, fmt.Errorf("store: keyring entry %d has an empty key id", i)
		}
		if len(id) > 255 {
			return nil, fmt.Errorf("store: keyring key id %q is longer than 255 bytes", id)
		}
		key, err := decodeKey(material)
		if err != nil {
			return nil, fmt.Errorf("store: keyring entry %q: %w", id, err)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("store: keyring entry %q: %w", id, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("store: keyring entry %q: %w", id, err)
		}
		if _, dup := kr.byID[id]; dup {
			return nil, fmt.Errorf("store: keyring key id %q appears twice", id)
		}
		phoneCode, err := derivePhoneCodeKey(key)
		if err != nil {
			return nil, fmt.Errorf("store: keyring entry %q: %w", id, err)
		}
		auditPseudonym, err := deriveAuditPseudonymKey(key)
		if err != nil {
			return nil, fmt.Errorf("store: keyring entry %q: %w", id, err)
		}
		entry := kekEntry{id: id, aead: aead, phoneCode: phoneCode, auditPseudonym: auditPseudonym}
		kr.byID[id] = entry
		kr.ordered = append(kr.ordered, id)
		if len(kr.ordered) == 1 {
			kr.active = entry
		}
	}

	if len(kr.ordered) == 0 {
		return nil, errors.New("store: FOYER_KEK is set but contains no keys")
	}
	return kr, nil
}

func decodeKey(material string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err := enc.DecodeString(material); err == nil {
			if len(key) != 32 {
				return nil, fmt.Errorf("key is %d bytes, want 32 (AES-256)", len(key))
			}
			return key, nil
		}
	}
	return nil, errors.New("key is not valid base64")
}

// ActiveKeyID names the key new Seals will use.
func (k *Keyring) ActiveKeyID() string {
	if k == nil {
		return ""
	}
	return k.active.id
}

// Seal wraps plaintext for storage in the column named by aad.
//
// aad is the binding described at the top of this file. Callers use
// SecretContext to build it, so the same string is produced on Seal and on Open.
func (k *Keyring) Seal(plaintext, aad []byte) ([]byte, error) {
	if k == nil {
		return nil, ErrNoKeyring
	}

	dek := make([]byte, dekSize)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("store: generating data key: %w", err)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	dataAEAD, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	wrapNonce := make([]byte, k.active.aead.NonceSize())
	if _, err := rand.Read(wrapNonce); err != nil {
		return nil, fmt.Errorf("store: generating wrap nonce: %w", err)
	}
	dataNonce := make([]byte, dataAEAD.NonceSize())
	if _, err := rand.Read(dataNonce); err != nil {
		return nil, fmt.Errorf("store: generating data nonce: %w", err)
	}

	wrapped := k.active.aead.Seal(nil, wrapNonce, dek, aad)
	sealed := dataAEAD.Seal(nil, dataNonce, plaintext, aad)

	// magic | version | idLen | id | wrapNonce | wrappedLen(2) | wrapped | dataNonce | ciphertext
	out := make([]byte, 0, 3+1+1+len(k.active.id)+len(wrapNonce)+2+len(wrapped)+len(dataNonce)+len(sealed))
	out = append(out, envelopeMagic[:]...)
	out = append(out, envelopeVersion)
	out = append(out, byte(len(k.active.id)))
	out = append(out, k.active.id...)
	out = append(out, wrapNonce...)
	out = append(out, byte(len(wrapped)>>8), byte(len(wrapped)))
	out = append(out, wrapped...)
	out = append(out, dataNonce...)
	out = append(out, sealed...)
	return out, nil
}

// Open reverses Seal. It fails if the blob was sealed for a different row, a
// different column, or by a key this keyring does not hold.
func (k *Keyring) Open(blob, aad []byte) ([]byte, error) {
	if k == nil {
		return nil, ErrNoKeyring
	}
	if len(blob) == 0 {
		return nil, ErrSecretUnset
	}

	r := reader{b: blob}
	magic, ok := r.take(3)
	if !ok || magic[0] != envelopeMagic[0] || magic[1] != envelopeMagic[1] || magic[2] != envelopeMagic[2] {
		return nil, errors.New("store: sealed value is not a Foyer envelope")
	}
	version, ok := r.byte()
	if !ok || version != envelopeVersion {
		return nil, fmt.Errorf("store: sealed value has envelope version %d, want %d", version, envelopeVersion)
	}
	idLen, ok := r.byte()
	if !ok {
		return nil, errTruncated
	}
	idBytes, ok := r.take(int(idLen))
	if !ok {
		return nil, errTruncated
	}
	entry, known := k.byID[string(idBytes)]
	if !known {
		return nil, fmt.Errorf("store: sealed under key %q, which this keyring does not hold", idBytes)
	}

	wrapNonce, ok := r.take(entry.aead.NonceSize())
	if !ok {
		return nil, errTruncated
	}
	hi, ok1 := r.byte()
	lo, ok2 := r.byte()
	if !ok1 || !ok2 {
		return nil, errTruncated
	}
	wrapped, ok := r.take(int(hi)<<8 | int(lo))
	if !ok {
		return nil, errTruncated
	}

	dek, err := entry.aead.Open(nil, wrapNonce, wrapped, aad)
	if err != nil {
		return nil, fmt.Errorf("store: unwrapping data key: %w", err)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	dataAEAD, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	dataNonce, ok := r.take(dataAEAD.NonceSize())
	if !ok {
		return nil, errTruncated
	}
	plaintext, err := dataAEAD.Open(nil, dataNonce, r.rest(), aad)
	if err != nil {
		return nil, fmt.Errorf("store: opening sealed value: %w", err)
	}
	return plaintext, nil
}

var errTruncated = errors.New("store: sealed value is truncated")

// SecretContext builds the associated data that binds a ciphertext to one column of
// one row. Both sides of a round trip must produce the same string, so it is a
// function rather than a convention.
func SecretContext(table, column, rowID string) []byte {
	return []byte("foyer/" + table + "/" + column + "/" + rowID)
}

type reader struct {
	b []byte
	i int
}

func (r *reader) take(n int) ([]byte, bool) {
	if n < 0 || r.i+n > len(r.b) {
		return nil, false
	}
	out := r.b[r.i : r.i+n]
	r.i += n
	return out, true
}

func (r *reader) byte() (byte, bool) {
	v, ok := r.take(1)
	if !ok {
		return 0, false
	}
	return v[0], true
}

func (r *reader) rest() []byte { return r.b[r.i:] }
