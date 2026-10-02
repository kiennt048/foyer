package apistore

import (
	"context"
	"errors"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// The maintenance schedule and the guest Wi-Fi password — internal/api's
// ports_settings.go, over `store.MaintenanceRepo` and `store.GuestWifiKeyRepo`.

var (
	_ api.MaintenanceStore  = (*Adapter)(nil)
	_ api.GuestWifiKeyStore = (*Adapter)(nil)
)

func (a *Adapter) MaintenanceSettings(ctx context.Context) (gen.MaintenanceSettings, error) {
	m, err := a.st.Maintenance().Get(ctx)
	if err != nil {
		return gen.MaintenanceSettings{}, out(err)
	}
	return maintenanceOut(m), nil
}

// SetMaintenanceSettings stores the values and then puts them in force on this
// process's scheduler (Config.MaintenanceChanged). Other control planes read the row
// on their next cycle.
func (a *Adapter) SetMaintenanceSettings(ctx context.Context, v gen.MaintenanceSettingsValues, actor string) (gen.MaintenanceSettings, error) {
	m, err := a.st.Maintenance().Set(ctx, store.MaintenanceValues{
		IntervalSeconds:         v.IntervalSeconds,
		StaleAfterMissedUpdates: v.StaleAfterMissedUpdates,
		StaleCeilingPerPass:     v.StaleCeilingPerPass,
		RetentionBatchSize:      v.RetentionBatchSize,
	}, actor)
	if err != nil {
		return gen.MaintenanceSettings{}, out(err)
	}
	if a.maintenanceChanged != nil {
		a.maintenanceChanged(m.MaintenanceValues)
	}
	return maintenanceOut(m), nil
}

func maintenanceOut(m store.MaintenanceSettings) gen.MaintenanceSettings {
	lim := func(name string) gen.MaintenanceSettingLimit {
		l := store.MaintenanceLimits[name]
		return gen.MaintenanceSettingLimit{Minimum: l.Min, Maximum: l.Max, Default: l.Default}
	}
	o := gen.MaintenanceSettings{
		IntervalSeconds:         m.IntervalSeconds,
		StaleAfterMissedUpdates: m.StaleAfterMissedUpdates,
		StaleCeilingPerPass:     m.StaleCeilingPerPass,
		RetentionBatchSize:      m.RetentionBatchSize,
		IsDefault:               m.IsDefault,
		UpdatedBy:               m.UpdatedBy,
	}
	if m.UpdatedAt != nil {
		t := m.UpdatedAt.UTC()
		o.UpdatedAt = &t
	}
	o.Limits.IntervalSeconds = lim("intervalSeconds")
	o.Limits.StaleAfterMissedUpdates = lim("staleAfterMissedUpdates")
	o.Limits.StaleCeilingPerPass = lim("staleCeilingPerPass")
	o.Limits.RetentionBatchSize = lim("retentionBatchSize")
	return o
}

// ── the guest Wi-Fi password ─────────────────────────────────────────────────

// siteExists answers ErrNotFound for a site this deployment does not have, so a
// status read for an unknown site is a 404 rather than `set: false`.
func (a *Adapter) siteExists(ctx context.Context, siteID string) error {
	_, err := a.st.Sites().Get(ctx, siteID)
	return out(err)
}

func (a *Adapter) GuestWifiKey(ctx context.Context, siteID string) (gen.GuestWifiKeyStatus, error) {
	if err := a.siteExists(ctx, siteID); err != nil {
		return gen.GuestWifiKeyStatus{}, err
	}
	st, err := a.st.GuestWifiKeys().Status(ctx, siteID)
	if err != nil {
		return gen.GuestWifiKeyStatus{}, out(err)
	}
	return wifiKeyOut(st), nil
}

func (a *Adapter) SetGuestWifiKey(ctx context.Context, siteID string, passphrase *string, actor string) (gen.GuestWifiKeyStatus, error) {
	if err := a.siteExists(ctx, siteID); err != nil {
		return gen.GuestWifiKeyStatus{}, err
	}
	var err error
	if passphrase == nil {
		err = a.st.GuestWifiKeys().Clear(ctx, siteID)
	} else {
		err = a.st.GuestWifiKeys().Set(ctx, siteID, *passphrase, actor)
	}
	if errors.Is(err, store.ErrNotAWifiPassphrase) {
		// The handler refuses first; reaching this is the two rules disagreeing.
		return gen.GuestWifiKeyStatus{}, errors.New("apistore: the store refused a passphrase the API accepted")
	}
	if err != nil {
		return gen.GuestWifiKeyStatus{}, out(err)
	}
	return a.GuestWifiKey(ctx, siteID)
}

func (a *Adapter) VoucherSlipWifi(ctx context.Context, siteID string) (gen.VoucherSlipWifi, error) {
	if err := a.siteExists(ctx, siteID); err != nil {
		return gen.VoucherSlipWifi{}, err
	}
	p, err := a.st.GuestWifiKeys().Reveal(ctx, siteID)
	if err != nil {
		return gen.VoucherSlipWifi{}, out(err)
	}
	if p == "" {
		return gen.VoucherSlipWifi{}, nil
	}
	return gen.VoucherSlipWifi{Passphrase: &p}, nil
}

func wifiKeyOut(st store.GuestWifiKeyStatus) gen.GuestWifiKeyStatus {
	o := gen.GuestWifiKeyStatus{Set: st.Set, UpdatedBy: st.UpdatedBy}
	if st.UpdatedAt != nil {
		t := st.UpdatedAt.UTC()
		o.UpdatedAt = &t
	}
	return o
}
