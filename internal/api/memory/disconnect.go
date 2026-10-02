package memory

import (
	"context"
	"strings"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// The fake's half of `disconnectSession`.
//
// **Still a fake, and still labelled one.** Nothing here is evidence about a
// device: the RFC 5176 exchange is proven in `internal/nas` against a UDP
// listener, the controller call against `httptest`, and the adapter against
// PostgreSQL. What this proves is the API layer — the order of the steps, the
// refusals, the audit entry, the shape of the answer.

var _ api.GuestAccess = (*Store)(nil)

// CanEndSession answers from the device's stored `canDeauthGuest`.
//
// The real adapter reads the *driver* rather than the column; this fake
// has no driver, so the column is all it has. `CanEndSessionErr` overrides it
// for a test that needs the two to disagree.
func (s *Store) CanEndSession(_ context.Context, sessionID string) error {
	if s.CanEndSessionErr != nil {
		return s.CanEndSessionErr(sessionID)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessionLocked(sessionID)
	if !ok {
		return api.ErrNotFound
	}
	for _, n := range s.world.Nas {
		if n.Id != sess.NasId {
			continue
		}
		if !n.Capabilities.CanDeauthGuest {
			return &api.DisconnectRefusal{Driver: n.Driver, NasName: n.Name}
		}
		return nil
	}
	// A session on a device this world does not have is a device no driver can
	// be built for, which is the refusal and not a pass.
	return api.ErrDeviceCannotDisconnect
}

func (s *Store) sessionLocked(id string) (gen.Session, bool) {
	for _, sess := range s.world.Sessions {
		if sess.Id == id {
			return sess, true
		}
	}
	return gen.Session{}, false
}

// RevokeGrantsForSession revokes the open portal grants for this session's MAC at
// its venue, in the portal store the fake's portal writes to.
func (s *Store) RevokeGrantsForSession(_ context.Context, sessionID string) (int, error) {
	s.mu.RLock()
	sess, ok := s.sessionLocked(sessionID)
	s.mu.RUnlock()
	if !ok {
		return 0, api.ErrNotFound
	}
	if s.PortalStore == nil {
		return 0, nil
	}
	now := s.clock()
	n := 0
	for _, g := range s.PortalStore.Grants() {
		if g.SiteID != sess.SiteId || !sameMAC(g.ClientMAC, sess.CallingStationId) {
			continue
		}
		if g.RevokedAt != nil || (g.ExpiresAt != nil && !g.ExpiresAt.After(now)) {
			continue
		}
		s.PortalStore.Revoke(g.ID, now)
		n++
	}
	return n, nil
}

// VoucherForSession names the voucher a session was admitted with: the session's
// own `voucherId`, else a voucher pointing at this session, else one whose code is
// the RADIUS `User-Name`, else an open grant's. A voucher already revoked is not
// named — there is nothing left to revoke.
func (s *Store) VoucherForSession(_ context.Context, sessionID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessionLocked(sessionID)
	if !ok {
		return "", api.ErrNotFound
	}
	live := func(id string) bool {
		for _, v := range s.world.Vouchers {
			if v.Id == id {
				return v.State != gen.VoucherStateRevoked
			}
		}
		return false
	}
	if sess.VoucherId != nil && live(*sess.VoucherId) {
		return *sess.VoucherId, nil
	}
	for _, v := range s.world.Vouchers {
		if v.State == gen.VoucherStateRevoked {
			continue
		}
		if v.CurrentSessionId != nil && *v.CurrentSessionId == sessionID {
			return v.Id, nil
		}
		if sess.Username != nil && strings.EqualFold(normaliseCode(v.Code), normaliseCode(*sess.Username)) {
			return v.Id, nil
		}
	}
	if s.PortalStore != nil {
		for _, g := range s.PortalStore.Grants() {
			if g.VoucherID != "" && g.RevokedAt == nil && g.SiteID == sess.SiteId &&
				sameMAC(g.ClientMAC, sess.CallingStationId) && live(g.VoucherID) {
				return g.VoucherID, nil
			}
		}
	}
	return "", nil
}

func sameMAC(a, b string) bool {
	norm := func(m string) string {
		return strings.NewReplacer(":", "", "-", "", ".", "").Replace(strings.ToLower(m))
	}
	return norm(a) != "" && norm(a) == norm(b)
}
