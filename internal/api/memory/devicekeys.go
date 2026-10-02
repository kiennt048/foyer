package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/kiennt048/foyer/internal/api"
)

// The trusted-device key ring, over the world. `device_cookie_keys` in the real
// store (migration 00053), where each key is sealed under FOYER_KEK; here it is a
// slice that outlives every `api.Server` built over the same Store, which is what
// lets a test restart the server and keep the ring — the property that keeps a
// restart from untrusting every device at once.

type memDeviceKey struct {
	api.DeviceCookieKey
	created time.Time
}

// DeviceCookieKeys is `api.DeviceCookieKeyStore`: newest first, a new key minted
// when there is none or the newest is older than rotateAfter, and a retired key
// dropped once its successor has been active for keepFor.
func (s *Store) DeviceCookieKeys(_ context.Context, now time.Time, rotateAfter, keepFor time.Duration) ([]api.DeviceCookieKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.deviceKeys) == 0 || now.Sub(s.deviceKeys[0].created) >= rotateAfter {
		key := make([]byte, 32)
		id := make([]byte, 8)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if _, err := rand.Read(id); err != nil {
			return nil, err
		}
		s.deviceKeys = append([]memDeviceKey{{
			DeviceCookieKey: api.DeviceCookieKey{ID: hex.EncodeToString(id), Key: key},
			created:         now,
		}}, s.deviceKeys...)
	}
	kept := s.deviceKeys[:1]
	for i := 1; i < len(s.deviceKeys); i++ {
		// Retired when its successor (the next newer key) arrived.
		if now.Sub(s.deviceKeys[i-1].created) < keepFor {
			kept = append(kept, s.deviceKeys[i])
		}
	}
	s.deviceKeys = kept
	out := make([]api.DeviceCookieKey, len(kept))
	for i, k := range kept {
		out[i] = api.DeviceCookieKey{ID: k.ID, Key: append([]byte(nil), k.Key...)}
	}
	return out, nil
}
