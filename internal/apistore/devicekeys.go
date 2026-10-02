package apistore

import (
	"context"
	"time"

	"github.com/kiennt048/foyer/internal/api"
)

// DeviceCookieKeys is `api.DeviceCookieKeyStore` over `device_cookie_keys`
// (migration 00053): the trusted-device key ring, each key sealed under FOYER_KEK,
// read, minted and pruned in one transaction by `store.DeviceCookieKeyRepo.Ring`.
func (a *Adapter) DeviceCookieKeys(ctx context.Context, now time.Time, rotateAfter, keepFor time.Duration) ([]api.DeviceCookieKey, error) {
	ring, err := a.st.DeviceCookieKeys().Ring(ctx, now, rotateAfter, keepFor)
	if err != nil {
		return nil, err
	}
	out := make([]api.DeviceCookieKey, len(ring))
	for i, k := range ring {
		out[i] = api.DeviceCookieKey{ID: k.ID, Key: k.Key}
	}
	return out, nil
}
