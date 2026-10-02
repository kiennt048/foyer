package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/nas"
)

// The `network` tag: transport and RADIUS-plane settings an operator sets in the
// console rather than with an environment variable, a file, a constant or
// `foyerctl`. Reads are `nas:read`, writes `nas:manage` — the administrator's,
// like every other change to how the network is reached — and every write is
// audited and followed by `ReloadNetwork`, which is what makes it take effect
// without a restart.
//
// **No answer here carries a private key.** A key arrives in an upload, is checked
// against its certificate, and is handed to the store to seal; the answer says
// `keySet: true` and nothing else about it.

// ── faults ───────────────────────────────────────────────────────────────────

type faultSet struct {
	errs   map[string]string
	faults map[string]gen.FieldFault
	params map[string]map[string]any
}

func newFaults() *faultSet {
	return &faultSet{errs: map[string]string{}, faults: map[string]gen.FieldFault{}, params: map[string]map[string]any{}}
}

func (f *faultSet) add(field string, fault gen.FieldFault, msg string, params map[string]any) {
	if _, seen := f.faults[field]; seen {
		return
	}
	f.errs[field], f.faults[field] = msg, fault
	if params != nil {
		f.params[field] = params
	}
}

func (f *faultSet) bound(field string, v int, min, max int) {
	switch {
	case v < min:
		f.add(field, gen.FieldFaultBelowMinimum, "Too small.", map[string]any{"minimumCount": min})
	case v > max:
		f.add(field, gen.FieldFaultAboveMaximum, "Too large.", map[string]any{"maximumCount": max})
	}
}

func (f *faultSet) certificate(field string, fault config.CertificateFault) {
	f.add(field, gen.FieldFaultCertificateRefused, "Refused: "+string(fault)+".", map[string]any{"reason": string(fault)})
}

func (f *faultSet) err() error {
	if len(f.faults) == 0 {
		return nil
	}
	return withFieldParams(validation(f.errs, f.faults), f.params)
}

// certificateFields maps a CertificateError's input onto the body's field names.
func certificateField(e *config.CertificateError, fields map[string]string) string {
	if f, ok := fields[e.Field]; ok {
		return f
	}
	return "certificatePem"
}

func actorOf(ctx context.Context) (string, *caller) {
	who := callerFrom(ctx)
	if who == nil {
		return "", nil
	}
	return who.Operator.Username, who
}

var networkSubject = gen.AuditSubject{Kind: gen.AuditSubjectKindNetworkSettings, Id: "network-settings"}
var radiusSubject = gen.AuditSubject{Kind: gen.AuditSubjectKindRadiusServer, Id: "radius-server"}

// ── the portal address, the throttles and the ceilings ──────────────────────

func urlSetting(v string, src gen.NetworkSettingSource) gen.NetworkUrlSetting {
	if v == "" {
		return gen.NetworkUrlSetting{Source: gen.NetworkSettingSourceUnset}
	}
	return gen.NetworkUrlSetting{Value: ptr(v), Source: src}
}

func (s *Server) networkSettingsView(ctx context.Context) (gen.NetworkSettings, error) {
	stored, err := s.ports.Network.StoredNetwork(ctx)
	if err != nil {
		return gen.NetworkSettings{}, err
	}
	eff := s.network()
	return gen.NetworkSettings{
		PortalUrl:         urlSetting(eff.PortalURL, eff.PortalURLSource),
		VenueUrl:          urlSetting(eff.VenueURL, eff.VenueURLSource),
		Throttles:         eff.Throttles,
		BodyCeilings:      eff.Ceilings,
		Bounds:            s.networkBounds(),
		Defaults:          s.networkDefaults(),
		PortalCertificate: s.portalCertificateStatus(),
		UpdatedAt:         stored.UpdatedAt,
		UpdatedBy:         stored.UpdatedBy,
	}, nil
}

func (s *Server) GetNetworkSettings(ctx context.Context, _ gen.GetNetworkSettingsRequestObject) (gen.GetNetworkSettingsResponseObject, error) {
	if s.ports.Network == nil {
		return nil, errNotImplemented
	}
	v, err := s.networkSettingsView(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetNetworkSettings200JSONResponse(v), nil
}

// cleanURL trims a URL setting; nil stays nil, which is "go back to the
// deployment's own value".
func cleanURL(v *string) *string {
	if v == nil {
		return nil
	}
	t := strings.TrimSpace(*v)
	return &t
}

func (s *Server) UpdateNetworkSettings(ctx context.Context, request gen.UpdateNetworkSettingsRequestObject) (gen.UpdateNetworkSettingsResponseObject, error) {
	if s.ports.Network == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body
	in.PortalUrl, in.VenueUrl = cleanURL(in.PortalUrl), cleanURL(in.VenueUrl)

	f := newFaults()
	for field, v := range map[string]*string{"portalUrl": in.PortalUrl, "venueUrl": in.VenueUrl} {
		if v != nil && config.CheckWebAddress(*v) != nil {
			f.add(field, gen.FieldFaultNotAWebAddress, "Not an http:// or https:// address.", nil)
		}
	}
	// **The address and the certificate have to agree**, because RFC 8908 hands
	// the operating system this address and a client refuses a certificate that
	// does not name it. Checked against the console's pair; a certificate file the
	// deployment supplies is checked by the listener that loads it.
	if _, bad := f.faults["portalUrl"]; !bad {
		next := s.net.bootstrap.PortalURL
		if in.PortalUrl != nil {
			next = *in.PortalUrl
		}
		if pair := s.network().PortalCertificate; pair != nil {
			var ce *config.CertificateError
			if err := config.CheckPortalPair(pair.Certificate.Leaf, next); errors.As(err, &ce) {
				f.certificate("portalUrl", ce.Fault)
			}
		}
	}
	for name, t := range map[string]gen.SignInThrottle{
		"consoleAddress": in.Throttles.ConsoleAddress, "consoleAccount": in.Throttles.ConsoleAccount,
		"portalAddress": in.Throttles.PortalAddress, "portalAccount": in.Throttles.PortalAccount,
	} {
		f.bound("throttles."+name+".burst", t.Burst, throttleBurstMin, throttleBurstMax)
		f.bound("throttles."+name+".intervalMs", t.IntervalMs, throttleIntervalMinMs, throttleIntervalMaxMs)
	}
	// The ceilings' maxima are this container's (memplan.go), the same
	// figures the read offers as bounds.
	c, b := in.BodyCeilings, s.networkBounds()
	f.bound("bodyCeilings.portalBytes", c.PortalBytes, b.PortalBytes.Minimum, b.PortalBytes.Maximum)
	f.bound("bodyCeilings.consoleBytes", c.ConsoleBytes, b.ConsoleBytes.Minimum, b.ConsoleBytes.Maximum)
	f.bound("bodyCeilings.anonymousBytes", c.AnonymousBytes, b.AnonymousBytes.Minimum, b.AnonymousBytes.Maximum)
	f.bound("bodyCeilings.headerBytes", c.HeaderBytes, headerBytesMin, MaxHeaderCeiling)
	if err := f.err(); err != nil {
		return nil, err
	}

	before := *s.network()
	actor, who := actorOf(ctx)
	if err := s.ports.Network.SaveTransport(ctx, StoredTransport{
		PortalURL: in.PortalUrl, VenueURL: in.VenueUrl, Throttles: in.Throttles, BodyCeilings: in.BodyCeilings,
	}, actor); err != nil {
		return nil, err
	}
	if err := s.ReloadNetwork(ctx); err != nil {
		return nil, err
	}
	after := *s.network()

	s.record(ctx, who, gen.AuditKindNetworkSettingsChanged, networkSubject, map[string]any{"changed": transportChanges(before, after)})
	v, err := s.networkSettingsView(ctx)
	if err != nil {
		return nil, err
	}
	return gen.UpdateNetworkSettings200JSONResponse(v), nil
}

// transportChanges is `from`/`to` per changed value, flattened, for the audit
// entry: a per-address throttle raised from 30 to 1 000 is the change a reader of
// the log most needs to find.
func transportChanges(a, b EffectiveNetwork) map[string]any {
	out := map[string]any{}
	diff := func(k string, x, y any) {
		if x != y {
			out[k] = map[string]any{"from": x, "to": y}
		}
	}
	diff("portalUrl", a.PortalURL, b.PortalURL)
	diff("venueUrl", a.VenueURL, b.VenueURL)
	for _, t := range []struct {
		k    string
		x, y gen.SignInThrottle
	}{
		{"consoleAddress", a.Throttles.ConsoleAddress, b.Throttles.ConsoleAddress},
		{"consoleAccount", a.Throttles.ConsoleAccount, b.Throttles.ConsoleAccount},
		{"portalAddress", a.Throttles.PortalAddress, b.Throttles.PortalAddress},
		{"portalAccount", a.Throttles.PortalAccount, b.Throttles.PortalAccount},
	} {
		diff("throttles."+t.k+".burst", t.x.Burst, t.y.Burst)
		diff("throttles."+t.k+".intervalMs", t.x.IntervalMs, t.y.IntervalMs)
	}
	diff("bodyCeilings.portalBytes", a.Ceilings.PortalBytes, b.Ceilings.PortalBytes)
	diff("bodyCeilings.consoleBytes", a.Ceilings.ConsoleBytes, b.Ceilings.ConsoleBytes)
	diff("bodyCeilings.anonymousBytes", a.Ceilings.AnonymousBytes, b.Ceilings.AnonymousBytes)
	diff("bodyCeilings.headerBytes", a.Ceilings.HeaderBytes, b.Ceilings.HeaderBytes)
	return out
}

// ── the portal certificate ──────────────────────────────────────────────────

func (s *Server) PutPortalCertificate(ctx context.Context, request gen.PutPortalCertificateRequestObject) (gen.PutPortalCertificateResponseObject, error) {
	if s.ports.Network == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body
	f := newFaults()
	if strings.TrimSpace(in.CertificatePem) == "" {
		f.add("certificatePem", gen.FieldFaultRequired, "Required.", nil)
	}
	if in.PrivateKeyPem == nil || strings.TrimSpace(*in.PrivateKeyPem) == "" {
		f.add("privateKeyPem", gen.FieldFaultRequired, "Required.", nil)
	}
	portalURL := s.PortalURL()
	if in.PortalUrl != nil {
		portalURL = strings.TrimSpace(*in.PortalUrl)
		if config.CheckWebAddress(portalURL) != nil {
			f.add("portalUrl", gen.FieldFaultNotAWebAddress, "Not an http:// or https:// address.", nil)
		}
	}
	if err := f.err(); err != nil {
		return nil, err
	}

	pair, info, err := config.ParseCertificatePair([]byte(in.CertificatePem), []byte(*in.PrivateKeyPem), s.now())
	var ce *config.CertificateError
	if errors.As(err, &ce) {
		f.certificate(certificateField(ce, map[string]string{
			config.CertFieldCertificate: "certificatePem", config.CertFieldKey: "privateKeyPem",
		}), ce.Fault)
		return nil, f.err()
	}
	if err != nil {
		return nil, err
	}
	if err := config.CheckPortalPair(pair.Leaf, portalURL); errors.As(err, &ce) {
		f.certificate("portalUrl", ce.Fault)
		return nil, f.err()
	}

	actor, who := actorOf(ctx)
	var saveURL *string
	if in.PortalUrl != nil {
		saveURL = &portalURL
	}
	if err := s.ports.Network.PutPortalCertificate(ctx, in.CertificatePem, []byte(*in.PrivateKeyPem), saveURL, actor); err != nil {
		if errors.Is(err, ErrKeyringAbsent) {
			return nil, errNotImplemented
		}
		return nil, err
	}
	if err := s.ReloadNetwork(ctx); err != nil {
		return nil, err
	}
	params := map[string]any{"fingerprintSha256": info.FingerprintSHA256, "notAfter": info.NotAfter.Format(time.RFC3339)}
	if saveURL != nil {
		params["portalUrl"] = portalURL
	}
	s.record(ctx, who, gen.AuditKindPortalCertificateChanged, networkSubject, params)
	return gen.PutPortalCertificate200JSONResponse(s.portalCertificateStatus()), nil
}

func (s *Server) DeletePortalCertificate(ctx context.Context, _ gen.DeletePortalCertificateRequestObject) (gen.DeletePortalCertificateResponseObject, error) {
	if s.ports.Network == nil {
		return nil, errNotImplemented
	}
	if err := s.ports.Network.DeletePortalCertificate(ctx); err != nil {
		return nil, err
	}
	if err := s.ReloadNetwork(ctx); err != nil {
		return nil, err
	}
	_, who := actorOf(ctx)
	s.record(ctx, who, gen.AuditKindPortalCertificateChanged, networkSubject, map[string]any{"removed": true})
	return gen.DeletePortalCertificate200JSONResponse(s.portalCertificateStatus()), nil
}

// ── FreeRADIUS ──────────────────────────────────────────────────────────────

func (s *Server) radiusView(ctx context.Context) (gen.RadiusServerSettings, error) {
	stored, err := s.ports.Network.StoredNetwork(ctx)
	if err != nil {
		return gen.RadiusServerSettings{}, err
	}
	out := gen.RadiusServerSettings{
		MaxServers:      DefaultRadiusMaxServers,
		MaxServersBound: radiusMaxServersBound(),
		EapTlsEnabled:   stored.EapTLSEnabled,
		ApplyAvailable:  s.ports.Network.ApplyAvailable(),
	}
	if stored.RadiusMaxServers != nil {
		out.MaxServers = *stored.RadiusMaxServers
	}
	eap, err := s.ports.Network.EapCertificate(ctx)
	switch {
	case err == nil:
		out.EapCertificate = describeEap(eap)
	case errors.Is(err, ErrNotFound):
	default:
		return gen.RadiusServerSettings{}, err
	}
	out.LastSync, out.LastApply, err = s.ports.Network.LastRadiusOperations(ctx)
	if err != nil {
		return gen.RadiusServerSettings{}, err
	}
	// Pending: a generator setting changed after the last apply that reached the
	// running server. An apply that wrote the tree and could not restart did not.
	if stored.RadiusChangedAt != nil {
		out.PendingApply = out.LastApply == nil || out.LastApply.Outcome != gen.RadiusApplyOutcomeApplied ||
			out.LastApply.At.Before(*stored.RadiusChangedAt)
	}
	return out, nil
}

func describeEap(c StoredCertificate) gen.EapCertificateStatus {
	st := gen.EapCertificateStatus{KeySet: true, UpdatedAt: ptr(c.UpdatedAt)}
	if chain, err := config.ParseCertificates(config.CertFieldCertificate, []byte(c.CertificatePEM)); err == nil {
		info := config.DescribeCertificate(chain[0], len(chain))
		st.Subject, st.Issuer = ptr(info.Subject), ptr(info.Issuer)
		st.NotAfter, st.FingerprintSha256 = ptr(info.NotAfter), ptr(info.FingerprintSHA256)
	}
	if ca, err := config.ParseCertificates(config.CertFieldCA, []byte(c.CAPEM)); err == nil {
		st.CaSubject = ptr(ca[0].Subject.String())
	}
	return st
}

func (s *Server) GetRadiusServerSettings(ctx context.Context, _ gen.GetRadiusServerSettingsRequestObject) (gen.GetRadiusServerSettingsResponseObject, error) {
	if s.ports.Network == nil {
		return nil, errNotImplemented
	}
	v, err := s.radiusView(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetRadiusServerSettings200JSONResponse(v), nil
}

func (s *Server) UpdateRadiusServerSettings(ctx context.Context, request gen.UpdateRadiusServerSettingsRequestObject) (gen.UpdateRadiusServerSettingsResponseObject, error) {
	if s.ports.Network == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body
	f := newFaults()
	f.bound("maxServers", in.MaxServers, radiusMaxServersMin, radiusMaxServersMax)
	if in.EapTlsEnabled {
		if _, err := s.ports.Network.EapCertificate(ctx); errors.Is(err, ErrNotFound) {
			f.add("eapTlsEnabled", gen.FieldFaultRequired, "Upload an EAP-TLS certificate first.", nil)
		} else if err != nil {
			return nil, err
		}
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	before, err := s.radiusView(ctx)
	if err != nil {
		return nil, err
	}
	actor, who := actorOf(ctx)
	if err := s.ports.Network.SaveRadius(ctx, in.MaxServers, in.EapTlsEnabled, actor); err != nil {
		return nil, err
	}
	changed := map[string]any{}
	if before.MaxServers != in.MaxServers {
		changed["maxServers"] = map[string]any{"from": before.MaxServers, "to": in.MaxServers}
	}
	if before.EapTlsEnabled != in.EapTlsEnabled {
		changed["eapTlsEnabled"] = map[string]any{"from": before.EapTlsEnabled, "to": in.EapTlsEnabled}
	}
	s.record(ctx, who, gen.AuditKindNetworkSettingsChanged, radiusSubject, map[string]any{"changed": changed})
	v, err := s.radiusView(ctx)
	if err != nil {
		return nil, err
	}
	return gen.UpdateRadiusServerSettings200JSONResponse(v), nil
}

func (s *Server) PutEapCertificate(ctx context.Context, request gen.PutEapCertificateRequestObject) (gen.PutEapCertificateResponseObject, error) {
	if s.ports.Network == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body
	f := newFaults()
	if in.PrivateKeyPem == nil || strings.TrimSpace(*in.PrivateKeyPem) == "" {
		f.add("privateKeyPem", gen.FieldFaultRequired, "Required.", nil)
		return nil, f.err()
	}
	_, info, err := config.ParseCertificatePair([]byte(in.CertificatePem), []byte(*in.PrivateKeyPem), s.now())
	var ce *config.CertificateError
	if errors.As(err, &ce) {
		f.certificate(certificateField(ce, map[string]string{
			config.CertFieldCertificate: "certificatePem", config.CertFieldKey: "privateKeyPem",
		}), ce.Fault)
	} else if err != nil {
		return nil, err
	}
	if _, err := config.ParseCertificates(config.CertFieldCA, []byte(in.CaPem)); errors.As(err, &ce) {
		f.certificate("caPem", ce.Fault)
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	actor, who := actorOf(ctx)
	if err := s.ports.Network.PutEapCertificate(ctx, in.CertificatePem, []byte(*in.PrivateKeyPem), in.CaPem, actor); err != nil {
		if errors.Is(err, ErrKeyringAbsent) {
			return nil, errNotImplemented
		}
		return nil, err
	}
	s.record(ctx, who, gen.AuditKindNetworkSettingsChanged, radiusSubject, map[string]any{
		"eapCertificate": map[string]any{"fingerprintSha256": info.FingerprintSHA256, "notAfter": info.NotAfter.Format(time.RFC3339)},
	})
	v, err := s.radiusView(ctx)
	if err != nil {
		return nil, err
	}
	return gen.PutEapCertificate200JSONResponse(v), nil
}

func (s *Server) SyncRadiusClients(ctx context.Context, _ gen.SyncRadiusClientsRequestObject) (gen.SyncRadiusClientsResponseObject, error) {
	if s.ports.Network == nil {
		return nil, errNotImplemented
	}
	res, err := s.syncClients(ctx, gen.RadiusSyncTriggerOperator)
	if err != nil {
		return nil, err
	}
	return gen.SyncRadiusClients200JSONResponse(res), nil
}

// syncClients runs one sync of the client table and audits it.
func (s *Server) syncClients(ctx context.Context, trigger gen.RadiusSyncTrigger) (gen.RadiusClientSync, error) {
	actor, who := actorOf(ctx)
	res, err := s.ports.Network.SyncRadiusClients(ctx, trigger, actor)
	if err != nil {
		return gen.RadiusClientSync{}, err
	}
	params := map[string]any{
		"trigger": string(res.Trigger), "outcome": string(res.Outcome),
		"inserted": res.Inserted, "updated": res.Updated, "removed": len(res.Removed), "skipped": len(res.Skipped),
	}
	if res.ChangeClass != nil {
		params["changeClass"] = string(*res.ChangeClass)
	}
	s.record(ctx, who, gen.AuditKindRadiusClientsSynced, radiusSubject, params)
	return res, nil
}

// syncAfterClientWrite is the automatic sync after a RADIUS client changed. Its
// outcome is recorded and shown in the console; it never fails the write that
// triggered it, which has already happened.
func (s *Server) syncAfterClientWrite(ctx context.Context, trigger gen.RadiusSyncTrigger) {
	if s.ports.Network == nil {
		return
	}
	_, _ = s.syncClients(ctx, trigger)
}

func (s *Server) ApplyRadiusConfiguration(ctx context.Context, _ gen.ApplyRadiusConfigurationRequestObject) (gen.ApplyRadiusConfigurationResponseObject, error) {
	if s.ports.Network == nil {
		return nil, errNotImplemented
	}
	actor, who := actorOf(ctx)
	res, err := s.ports.Network.ApplyRadius(ctx, actor)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"outcome": string(res.Outcome)}
	if res.Restarted != nil {
		params["restarted"] = *res.Restarted
	}
	s.record(ctx, who, gen.AuditKindRadiusConfigurationApplied, radiusSubject, params)
	return gen.ApplyRadiusConfiguration200JSONResponse(res), nil
}

// ── CoA timing ──────────────────────────────────────────────────────────────

func coaDefaults() gen.CoaTimingValues {
	return gen.CoaTimingValues{TimeoutMs: int(nas.DefaultCoATimeout / time.Millisecond), Retries: nas.DefaultCoARetries}
}

func coaView(r CoaTimingRow) gen.NasCoaTiming {
	d := coaDefaults()
	out := gen.NasCoaTiming{
		NasId: r.NasID, SiteId: r.SiteID, Name: r.Name, Driver: r.Driver,
		TimeoutMs: d.TimeoutMs, Retries: d.Retries, Source: gen.NasCoaTimingSourceDefault,
	}
	if r.Stored != nil {
		out.TimeoutMs, out.Retries, out.Source = r.Stored.TimeoutMs, r.Stored.Retries, gen.NasCoaTimingSourceConsole
	}
	return out
}

func (s *Server) ListCoaTiming(ctx context.Context, _ gen.ListCoaTimingRequestObject) (gen.ListCoaTimingResponseObject, error) {
	if s.ports.Network == nil {
		return nil, errNotImplemented
	}
	rows, err := s.ports.Network.CoaTimings(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]gen.NasCoaTiming, 0, len(rows))
	for _, r := range rows {
		items = append(items, coaView(r))
	}
	return gen.ListCoaTiming200JSONResponse{Items: items, Defaults: coaDefaults(), Bounds: coaBounds()}, nil
}

func (s *Server) UpdateNasCoaTiming(ctx context.Context, request gen.UpdateNasCoaTimingRequestObject) (gen.UpdateNasCoaTimingResponseObject, error) {
	if s.ports.Network == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body
	var next *gen.CoaTimingValues
	if in.TimeoutMs != nil || in.Retries != nil {
		v := coaDefaults()
		if in.TimeoutMs != nil {
			v.TimeoutMs = *in.TimeoutMs
		}
		if in.Retries != nil {
			v.Retries = *in.Retries
		}
		f := newFaults()
		f.bound("timeoutMs", v.TimeoutMs, coaTimeoutMinMs, coaTimeoutMaxMs)
		f.bound("retries", v.Retries, coaRetriesMin, coaRetriesMax)
		if err := f.err(); err != nil {
			return nil, err
		}
		next = &v
	}
	row, err := s.ports.Network.SetCoaTiming(ctx, string(request.NasId), next)
	if errors.Is(err, ErrNoCoA) {
		return nil, validation(map[string]string{"nasId": "This device's driver does not send CoA itself."},
			map[string]gen.FieldFault{"nasId": gen.FieldFaultIncorrect})
	}
	if err != nil {
		return nil, err
	}
	view := coaView(row)
	_, who := actorOf(ctx)
	s.record(ctx, who, gen.AuditKindNetworkSettingsChanged, gen.AuditSubject{
		Kind: gen.AuditSubjectKindNas, Id: row.NasID, Name: ptr(row.Name),
	}, map[string]any{"timeoutMs": view.TimeoutMs, "retries": view.Retries, "source": string(view.Source)})
	return gen.UpdateNasCoaTiming200JSONResponse(view), nil
}
