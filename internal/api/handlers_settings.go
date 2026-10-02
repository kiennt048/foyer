package api

import (
	"context"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The maintenance schedule and the guest network's Wi-Fi password — two operator
// settings set in the console, like every other. Both writes are audited; the passphrase is write-only on every
// operation but the voucher slip's own read.

// ── maintenance ──────────────────────────────────────────────────────────────

func (s *Server) GetMaintenanceSettings(ctx context.Context, _ gen.GetMaintenanceSettingsRequestObject) (gen.GetMaintenanceSettingsResponseObject, error) {
	if s.ports.Maintenance == nil {
		return nil, errNotImplemented
	}
	m, err := s.ports.Maintenance.MaintenanceSettings(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetMaintenanceSettings200JSONResponse(m), nil
}

func (s *Server) UpdateMaintenanceSettings(ctx context.Context, request gen.UpdateMaintenanceSettingsRequestObject) (gen.UpdateMaintenanceSettingsResponseObject, error) {
	if s.ports.Maintenance == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	before, err := s.ports.Maintenance.MaintenanceSettings(ctx)
	if err != nil {
		return nil, err
	}
	in := *request.Body

	// The bounds are the store's, read from the port that enforces them, so the
	// refusal names the figure the store would have refused on.
	errs := map[string]string{}
	faults := map[string]gen.FieldFault{}
	params := map[string]map[string]any{}
	for _, f := range maintenanceFields(in, before) {
		switch {
		case f.value < f.limit.Minimum:
			errs[f.name], faults[f.name] = "Too small.", gen.FieldFaultBelowMinimum
			params[f.name] = map[string]any{"minimumCount": f.limit.Minimum}
		case f.value > f.limit.Maximum:
			errs[f.name], faults[f.name] = "Too large.", gen.FieldFaultAboveMaximum
			params[f.name] = map[string]any{"maximumCount": f.limit.Maximum}
		}
	}
	if len(faults) > 0 {
		return nil, withFieldParams(validation(errs, faults), params)
	}

	who := callerFrom(ctx)
	actor := ""
	if who != nil {
		actor = who.Operator.Username
	}
	after, err := s.ports.Maintenance.SetMaintenanceSettings(ctx, in, actor)
	if err != nil {
		return nil, err
	}

	changed := map[string]any{}
	for _, f := range maintenanceFields(in, before) {
		if f.value != f.was {
			changed[f.name] = map[string]any{"from": f.was, "to": f.value}
		}
	}
	s.record(ctx, who, gen.AuditKindMaintenanceSettingsChanged, gen.AuditSubject{
		Kind: gen.AuditSubjectKindMaintenanceSettings, Id: "maintenance-settings",
	}, map[string]any{"changed": changed})
	return gen.UpdateMaintenanceSettings200JSONResponse(after), nil
}

type maintenanceField struct {
	name       string
	value, was int
	limit      gen.MaintenanceSettingLimit
}

// maintenanceFields pairs each requested value with the one in force and its limit,
// by the contract's field name.
func maintenanceFields(in gen.MaintenanceSettingsValues, now gen.MaintenanceSettings) []maintenanceField {
	return []maintenanceField{
		{"intervalSeconds", in.IntervalSeconds, now.IntervalSeconds, now.Limits.IntervalSeconds},
		{"staleAfterMissedUpdates", in.StaleAfterMissedUpdates, now.StaleAfterMissedUpdates, now.Limits.StaleAfterMissedUpdates},
		{"staleCeilingPerPass", in.StaleCeilingPerPass, now.StaleCeilingPerPass, now.Limits.StaleCeilingPerPass},
		{"retentionBatchSize", in.RetentionBatchSize, now.RetentionBatchSize, now.Limits.RetentionBatchSize},
	}
}

// ── the guest network's Wi-Fi password ───────────────────────────────────────

func (s *Server) GetGuestWifiKey(ctx context.Context, request gen.GetGuestWifiKeyRequestObject) (gen.GetGuestWifiKeyResponseObject, error) {
	if s.ports.GuestWifi == nil {
		return nil, errNotImplemented
	}
	if _, err := s.siteNamed(ctx, request.SiteId); err != nil {
		return nil, err
	}
	st, err := s.ports.GuestWifi.GuestWifiKey(ctx, request.SiteId)
	if err != nil {
		return nil, err
	}
	return gen.GetGuestWifiKey200JSONResponse(st), nil
}

func (s *Server) UpdateGuestWifiKey(ctx context.Context, request gen.UpdateGuestWifiKeyRequestObject) (gen.UpdateGuestWifiKeyResponseObject, error) {
	if s.ports.GuestWifi == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	site, err := s.siteNamed(ctx, request.SiteId)
	if err != nil {
		return nil, err
	}
	p := request.Body.Passphrase
	if p != nil && !ValidWifiPassphrase(*p) {
		return nil, validation(map[string]string{"passphrase": "Not a Wi-Fi password."},
			map[string]gen.FieldFault{"passphrase": gen.FieldFaultNotAWifiPassphrase})
	}
	who := callerFrom(ctx)
	actor := ""
	if who != nil {
		actor = who.Operator.Username
	}
	st, err := s.ports.GuestWifi.SetGuestWifiKey(ctx, site.Id, p, actor)
	if err != nil {
		return nil, err
	}
	// The field that moved and whether it was cleared. Never the value, and not
	// its length either: a length is a fact about a secret.
	s.recordIn(ctx, who, site.Id, gen.AuditKindPortalSettingsChanged, gen.AuditSubject{
		Kind: gen.AuditSubjectKindSite, Id: site.Id, Name: ptr(site.Name),
	}, map[string]any{"fields": []string{"guestWifiKey"}, "cleared": p == nil})
	return gen.UpdateGuestWifiKey200JSONResponse(st), nil
}

func (s *Server) GetVoucherSlipWifi(ctx context.Context, request gen.GetVoucherSlipWifiRequestObject) (gen.GetVoucherSlipWifiResponseObject, error) {
	if s.ports.GuestWifi == nil {
		return nil, errNotImplemented
	}
	if _, err := s.siteNamed(ctx, request.SiteId); err != nil {
		return nil, err
	}
	w, err := s.ports.GuestWifi.VoucherSlipWifi(ctx, request.SiteId)
	if err != nil {
		return nil, err
	}
	return gen.GetVoucherSlipWifi200JSONResponse(w), nil
}
