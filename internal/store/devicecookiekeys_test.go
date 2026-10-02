package store_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

// TestTheDeviceCookieKeyRingSurvivesARestartSealed is the device-cookie key ring at
// the storage end: a trusted-device key drawn when the process starts would
// untrust every device on every restart. The ring is persisted, sealed under FOYER_KEK,
// and a second Store opened on the same database — a restarted process — reads
// the same key back.
func TestTheDeviceCookieKeyRingSurvivesARestartSealed(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	kr := testKeyring(t)
	open := func() *store.Store {
		st, err := store.Open(ctx, store.Config{URL: ownerURL(t), Keys: kr, MaxConns: 2})
		fatalf(t, err, "opening store")
		return st
	}
	const rotate, keep = 90 * 24 * time.Hour, 180 * 24 * time.Hour
	t0 := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	first := open()
	ring, err := first.DeviceCookieKeys().Ring(ctx, t0, rotate, keep)
	fatalf(t, err, "reading the ring")
	if len(ring) != 1 || len(ring[0].Key) != 32 {
		t.Fatalf("a fresh ring = %d keys, want one of 32 bytes", len(ring))
	}
	signing := ring[0]
	first.Close()

	var raw []byte
	fatalf(t, ownerPool(t).QueryRow(ctx, `SELECT key_sealed FROM device_cookie_keys WHERE id = $1`, signing.ID).Scan(&raw), "raw read")
	if bytes.Contains(raw, signing.Key) {
		t.Fatal("device_cookie_keys.key_sealed holds the key in the clear")
	}

	// The restart.
	second := open()
	defer second.Close()
	ring, err = second.DeviceCookieKeys().Ring(ctx, t0.Add(24*time.Hour), rotate, keep)
	fatalf(t, err, "reading the ring after a restart")
	if len(ring) != 1 || ring[0].ID != signing.ID || !bytes.Equal(ring[0].Key, signing.Key) {
		t.Fatalf("after a restart the ring is %d keys, first %q; want the same single key %q", len(ring), ring[0].ID, signing.ID)
	}

	// Ninety-one days on, a new key signs and the old one still verifies.
	ring, err = second.DeviceCookieKeys().Ring(ctx, t0.Add(91*24*time.Hour), rotate, keep)
	fatalf(t, err, "rotating")
	if len(ring) != 2 || ring[0].ID == signing.ID || ring[1].ID != signing.ID {
		t.Fatalf("after ninety-one days the ring is %v, want [new, %s]", ids(ring), signing.ID)
	}
	// A cookie lifetime after the rotation, the retired key is gone from the table.
	ring, err = second.DeviceCookieKeys().Ring(ctx, t0.Add((91+180)*24*time.Hour), rotate, keep)
	fatalf(t, err, "pruning")
	var left int
	fatalf(t, ownerPool(t).QueryRow(ctx, `SELECT count(*) FROM device_cookie_keys WHERE id = $1`, signing.ID).Scan(&left), "count")
	// (The second key is past ninety days by then too, so a third now signs.)
	if left != 0 || len(ring) != 2 || ring[0].ID == signing.ID || ring[1].ID == signing.ID {
		t.Fatalf("a key retired 180 days ago is still held (rows %d, ring %v)", left, ids(ring))
	}
}

// TestADeviceCookieKeyUnderAnAbsentKEKIsReplacedNotFatal: the KEK that sealed the
// ring left FOYER_KEK. Sign-in must not fail on it; a new key is minted, which
// untrusts every device once — the fail-safe direction.
func TestADeviceCookieKeyUnderAnAbsentKEKIsReplacedNotFatal(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	const rotate, keep = 90 * 24 * time.Hour, 180 * 24 * time.Hour

	a, err := store.Open(ctx, store.Config{URL: ownerURL(t), Keys: testKeyring(t), MaxConns: 2})
	fatalf(t, err, "opening store")
	old, err := a.DeviceCookieKeys().Ring(ctx, t0, rotate, keep)
	fatalf(t, err, "first ring")
	a.Close()

	b, err := store.Open(ctx, store.Config{URL: ownerURL(t), Keys: testKeyring(t), MaxConns: 2})
	fatalf(t, err, "opening store with another KEK")
	defer b.Close()
	ring, err := b.DeviceCookieKeys().Ring(ctx, t0.Add(time.Hour), rotate, keep)
	fatalf(t, err, "a ring under another KEK must not be an error")
	if len(ring) != 1 || ring[0].ID == old[0].ID {
		t.Fatalf("under another KEK the ring is %v, want one freshly minted key", ids(ring))
	}
}

func ids(ring []store.DeviceCookieKey) []string {
	out := make([]string, len(ring))
	for i, k := range ring {
		out[i] = k.ID
	}
	return out
}
