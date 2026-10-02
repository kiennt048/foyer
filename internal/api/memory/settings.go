package memory

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// The maintenance and guest Wi-Fi ports, in memory. **Fakes, and labelled ones**:
// the real settings live in `internal/store` (maintenance.go, guestwifi.go) and are
// proven against PostgreSQL there. The limits are a copy of
// `store.MaintenanceLimits`, because this package does not import the store; the
// differential harness compares this fake, the adapter and `web/mock`.

var (
	_ api.MaintenanceStore  = (*Store)(nil)
	_ api.GuestWifiKeyStore = (*Store)(nil)
)

type memSettings struct {
	maintenance   *gen.MaintenanceSettingsValues
	maintenanceAt *time.Time
	maintenanceBy *string
	wifi          map[string]memWifiKey
}

type memWifiKey struct {
	passphrase string
	at         time.Time
	by         *string
}

var memMaintenanceLimits = struct {
	interval, stale, ceiling, batch gen.MaintenanceSettingLimit
}{
	interval: gen.MaintenanceSettingLimit{Minimum: 60, Maximum: 86400, Default: 300},
	stale:    gen.MaintenanceSettingLimit{Minimum: 3, Maximum: 48, Default: 4},
	ceiling:  gen.MaintenanceSettingLimit{Minimum: 1, Maximum: 100000, Default: 200},
	batch:    gen.MaintenanceSettingLimit{Minimum: 50, Maximum: 10000, Default: 500},
}

func (s *Store) MaintenanceSettings(context.Context) (gen.MaintenanceSettings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maintenanceLocked(), nil
}

func (s *Store) maintenanceLocked() gen.MaintenanceSettings {
	l := memMaintenanceLimits
	out := gen.MaintenanceSettings{
		IntervalSeconds: l.interval.Default, StaleAfterMissedUpdates: l.stale.Default,
		StaleCeilingPerPass: l.ceiling.Default, RetentionBatchSize: l.batch.Default,
		IsDefault: true,
	}
	if v := s.settings.maintenance; v != nil {
		out.IntervalSeconds, out.StaleAfterMissedUpdates = v.IntervalSeconds, v.StaleAfterMissedUpdates
		out.StaleCeilingPerPass, out.RetentionBatchSize = v.StaleCeilingPerPass, v.RetentionBatchSize
		out.IsDefault, out.UpdatedAt, out.UpdatedBy = false, s.settings.maintenanceAt, s.settings.maintenanceBy
	}
	out.Limits.IntervalSeconds = l.interval
	out.Limits.StaleAfterMissedUpdates = l.stale
	out.Limits.StaleCeilingPerPass = l.ceiling
	out.Limits.RetentionBatchSize = l.batch
	return out
}

func (s *Store) SetMaintenanceSettings(_ context.Context, v gen.MaintenanceSettingsValues, actor string) (gen.MaintenanceSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := s.clock().UTC()
	s.settings.maintenance, s.settings.maintenanceAt = &v, &at
	s.settings.maintenanceBy = nil
	if actor != "" {
		s.settings.maintenanceBy = &actor
	}
	return s.maintenanceLocked(), nil
}

func (s *Store) siteKnownLocked(siteID string) bool {
	return slices.ContainsFunc(s.world.Sites, func(x gen.Site) bool { return x.Id == siteID })
}

func (s *Store) GuestWifiKey(_ context.Context, siteID string) (gen.GuestWifiKeyStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.siteKnownLocked(siteID) {
		return gen.GuestWifiKeyStatus{}, api.ErrNotFound
	}
	return s.wifiStatusLocked(siteID), nil
}

func (s *Store) wifiStatusLocked(siteID string) gen.GuestWifiKeyStatus {
	k, ok := s.settings.wifi[siteID]
	if !ok {
		return gen.GuestWifiKeyStatus{}
	}
	at := k.at
	return gen.GuestWifiKeyStatus{Set: true, UpdatedAt: &at, UpdatedBy: k.by}
}

func (s *Store) SetGuestWifiKey(_ context.Context, siteID string, passphrase *string, actor string) (gen.GuestWifiKeyStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.siteKnownLocked(siteID) {
		return gen.GuestWifiKeyStatus{}, api.ErrNotFound
	}
	if passphrase == nil {
		delete(s.settings.wifi, siteID)
		return s.wifiStatusLocked(siteID), nil
	}
	if s.settings.wifi == nil {
		s.settings.wifi = map[string]memWifiKey{}
	}
	k := memWifiKey{passphrase: *passphrase, at: s.clock().UTC()}
	if actor != "" {
		k.by = &actor
	}
	s.settings.wifi[siteID] = k
	return s.wifiStatusLocked(siteID), nil
}

func (s *Store) VoucherSlipWifi(_ context.Context, siteID string) (gen.VoucherSlipWifi, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.siteKnownLocked(siteID) {
		return gen.VoucherSlipWifi{}, api.ErrNotFound
	}
	k, ok := s.settings.wifi[siteID]
	if !ok {
		return gen.VoucherSlipWifi{}, nil
	}
	p := k.passphrase
	return gen.VoucherSlipWifi{Passphrase: &p}, nil
}

// ── an account's 802.1X opt-in ───────────────────────────────────────────────

func has8021X(a gen.Account) bool { return strings.Contains(string(a.CredentialKind), "+bcrypt") }

// with8021X is the credential kind with the bcrypt verifier added or removed.
func with8021X(k gen.AccountCredentialKind, on bool) gen.AccountCredentialKind {
	base := strings.TrimSuffix(string(k), "+bcrypt")
	if on {
		return gen.AccountCredentialKind(base + "+bcrypt")
	}
	return gen.AccountCredentialKind(base)
}

// papHolderLocked: another account, at any venue, opted in with this username.
// `account_pap_verifiers.username` is globally unique in the store.
func (s *Store) papHolderLocked(username, exceptID string) bool {
	return slices.ContainsFunc(s.world.Accounts, func(a gen.Account) bool {
		return a.Id != exceptID && a.Username == username && has8021X(a)
	})
}

// papPasswordOK is bcrypt's 72-byte input limit, as identity.HashPAPVerifier has it.
func papPasswordOK(p string) bool { return len(p) <= 72 }
