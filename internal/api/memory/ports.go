package memory

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/nas"
)

// Every port, implemented over the World.
//
// The filtering here is where a real adapter would build SQL, so the semantics
// are stated rather than improvised: which fields `q` matches per operation,
// what a site scope excludes, and what order rows come back in. Those are the
// three things a SQL rewrite is most likely to get subtly different, and they
// are what the differential harness compares.

var (
	_ api.SiteDirectory       = (*Store)(nil)
	_ api.ControllerRegistry  = (*Store)(nil)
	_ api.AccessPointRegistry = (*Store)(nil)
	_ api.DeviceOperations    = (*Store)(nil)
	_ api.VoucherStore        = (*Store)(nil)
	_ api.SessionLog          = (*Store)(nil)
	_ api.AccountDirectory    = (*Store)(nil)
	_ api.OperatorDirectory   = (*Store)(nil)
	_ api.AdminSessionStore   = (*Store)(nil)
	_ api.AuditLog            = (*Store)(nil)
	_ api.AuthAttemptLog      = (*Store)(nil)
	_ api.PortalSettingsStore = (*Store)(nil)
	_ api.WalledGardenStore   = (*Store)(nil)
	_ api.DashboardReader     = (*Store)(nil)
)

// ── sites ────────────────────────────────────────────────────────────────────

func (s *Store) Sites(context.Context) ([]gen.Site, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.world.Sites), nil
}

// CreateSite appends a site, ordered by name to match `SiteRepo.List`.
//
// **The ordering is not cosmetic here.** This fake is the differential harness's
// other implementation, and `listSites` is unpaginated, so a fake that appends and
// an adapter that `ORDER BY name` disagree on the response body for every world
// with more than one site — a diff the harness reports as a defect in whichever
// side it was not looking at.
func (s *Store) CreateSite(_ context.Context, in gen.SiteCreate) (gen.Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// One venue per install. The refusal is on
	// this operation and NOT on the world: the fixture world here has three sites
	// and goes on having them, exactly as `sites` goes on permitting many rows.
	//
	// It is here as well as in the adapter because this fake is the differential
	// harness's other implementation, and a refusal one side makes and the other
	// does not is reported as a defect in whichever side the harness was not
	// looking at.
	if len(s.world.Sites) > 0 {
		return gen.Site{}, api.ErrSiteExists
	}
	site := gen.Site{
		Id:   s.id("site-"),
		Name: strings.TrimSpace(in.Name),
		// `live`, because the contract does not accept an environment and
		// `sites.environment` defaults to it. A fake that chose `lab` here would
		// be the one place in the product where a created site is not a venue.
		Environment: gen.SiteEnvironmentLive,
	}
	s.world.Sites = append(s.world.Sites, site)
	slices.SortStableFunc(s.world.Sites, func(a, b gen.Site) int {
		return strings.Compare(a.Name, b.Name)
	})
	// The portal settings row is born beside the site, for the same reason the
	// adapter seeds one: a venue with no settings is a Portal screen reading
	// "Not found" and a guest with no context. This fake is the differential
	// harness's other implementation, so a row one side makes and the other
	// does not is reported as a product defect in whichever side the harness
	// was not looking at.
	portal := gen.PortalSettings{
		SiteId: site.Id, VenueName: site.Name,
		Languages: []gen.Language{gen.En},
		Methods:   []gen.PortalMethod{gen.PortalMethodVoucher},
	}
	portal.Terms.Summary = map[string]string{}
	s.world.Portal[site.Id] = portal
	return site, nil
}

// ── controllers ──────────────────────────────────────────────────────────────

// ControllersInSite returns the controllers an operator working in this site may
// see.
//
// A controller is bound through `vendorSites[].boundSiteId` rather than through
// a `siteId` of its own, because one controller manages many of the vendor's
// sites and an operator may adopt only some of them.
//
// **Visibility is derived from the binding and not from the probe**, as stated
// in full on `apistore.controllerServesSite`: a controller is listed when it is
// bound to this site, or when it is bound to nothing at all. Asking
// `len(c.VendorSites) == 0` instead would mean recording a controller's own
// sites removed it from every site view — including the one carrying the call
// that binds it. This port is one of the two servers `make diff-api` compares,
// so a rule that differs here is reported as a product defect that is really a
// defect in the oracle.
func (s *Store) ControllersInSite(_ context.Context, siteID string) ([]gen.Controller, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []gen.Controller{}
	for _, c := range s.world.Controllers {
		if siteID == "" || boundTo(c, siteID) || !boundAnywhere(c) {
			out = append(out, c)
		}
	}
	return out, nil
}

func boundTo(c gen.Controller, siteID string) bool {
	for _, vs := range c.VendorSites {
		if vs.BoundSiteId != nil && *vs.BoundSiteId == siteID {
			return true
		}
	}
	return false
}

// boundAnywhere is the proposition `len(c.VendorSites) == 0` was standing in for.
func boundAnywhere(c gen.Controller) bool {
	for _, vs := range c.VendorSites {
		if vs.BoundSiteId != nil {
			return true
		}
	}
	return false
}

func (s *Store) Controller(_ context.Context, id string) (gen.Controller, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.world.Controllers {
		if c.Id == id {
			return c, nil
		}
	}
	return gen.Controller{}, api.ErrNotFound
}

func (s *Store) RegisterController(_ context.Context, in gen.ControllerCreate, _ string) (gen.Controller, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := gen.Controller{
		Id:           s.id("ctl-"),
		Name:         in.Name,
		Vendor:       in.Vendor,
		BaseUrl:      in.BaseUrl,
		AuthMode:     in.AuthMode,
		TlsMode:      in.TlsMode,
		ControllerId: in.ControllerId,
		// Registered and never contacted: `unknown`, not `ok`. A registration
		// form cannot know whether the address resolves, the credential works
		// or the software is new enough — that is what `probeController` is for,
		// and claiming `ok` here would be the console asserting three things
		// nobody has checked.
		Reachability:  gen.NasReachabilityUnknown,
		CredentialSet: in.ApiKey != nil || in.Password != nil,
		CreatedAt:     ptr(s.clock()),
		VendorSites: []struct {
			BoundSiteId *string `json:"boundSiteId,omitempty"`
			Id          string  `json:"id"`
			Name        string  `json:"name"`
		}{},
	}
	s.world.Controllers = append([]gen.Controller{c}, s.world.Controllers...)
	return c, nil
}

func (s *Store) AmendController(_ context.Context, id string, in gen.ControllerUpdate) (gen.Controller, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.world.Controllers {
		c := &s.world.Controllers[i]
		if c.Id != id {
			continue
		}
		if in.Name != nil {
			c.Name = *in.Name
		}
		if in.BaseUrl != nil {
			c.BaseUrl = *in.BaseUrl
		}
		if in.ControllerId != nil {
			c.ControllerId = in.ControllerId
		}
		if in.TlsMode != nil {
			c.TlsMode = *in.TlsMode
		}
		if in.ApiKey != nil || in.Password != nil {
			c.CredentialSet = true
			// A replaced credential invalidates what the last probe learned.
			// Leaving `reachability: ok` would report a controller as working
			// on the strength of a credential that is no longer the one stored.
			c.Reachability = gen.NasReachabilityUnknown
		}
		return *c, nil
	}
	return gen.Controller{}, api.ErrNotFound
}

func (s *Store) ForgetController(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.world.Controllers {
		if c.Id == id {
			s.world.Controllers = append(s.world.Controllers[:i], s.world.Controllers[i+1:]...)
			return nil
		}
	}
	return api.ErrNotFound
}

func (s *Store) AccessPointsFrom(_ context.Context, controllerID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, ap := range s.world.Nas {
		if ap.ControllerId != nil && *ap.ControllerId == controllerID {
			n++
		}
	}
	return n, nil
}

// ── access points ────────────────────────────────────────────────────────────

// AccessPoints matches `q` against name, address, NAS-Identifier, model and
// driver — the fields an operator can read off a device or off a label.
func (s *Store) AccessPoints(_ context.Context, siteID, search string, w api.Window) (api.Page[gen.Nas], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []gen.Nas
	for _, n := range s.world.Nas {
		if siteID != "" && n.SiteId != siteID {
			continue
		}
		driver := string(n.Driver)
		if !matches(search, &n.Name, n.IpAddress, n.NasIdentifier, n.Model, &driver) {
			continue
		}
		out = append(out, n)
	}
	return slicePage(out, w), nil
}

func (s *Store) AccessPoint(_ context.Context, id string) (gen.Nas, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.world.Nas {
		if n.Id == id {
			return n, nil
		}
	}
	return gen.Nas{}, api.ErrNotFound
}

func (s *Store) RegisterAccessPoint(_ context.Context, in gen.NasCreate) (gen.Nas, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, err := newSecret()
	if err != nil {
		return gen.Nas{}, "", err
	}
	n := gen.Nas{
		Id:          s.id("nas-"),
		Name:        in.Name,
		SiteId:      in.SiteId,
		Driver:      in.Driver,
		Description: in.Description,
		// `NasCreate.ipAddress` is required and `Nas.ipAddress` is nullable: the
		// first is the `clients.conf` row's address, which FreeRADIUS matches
		// packets on, and the second is the access point's, which a device
		// discovered behind a controller need not have.
		IpAddress:     ptr(in.IpAddress),
		NasIdentifier: in.NasIdentifier,
		CoaPort:       in.CoaPort,
		// Kept only for the driver that reads it, which is what `apistore` writes
		// to `radius_clients.coa_vendor`. A `unifi` device carrying a CoA vendor
		// would be a value nothing reads, and the fake and the adapter have to
		// agree about that or `make diff-api` reports a difference that is only
		// the fake's.
		CoaVendor: coaVendorFor(in.Driver, in.CoaVendor),
		Enabled:   true,
		Source:    gen.Manual,
		// Nothing has been measured about this device yet. Capabilities are
		// measured, not declared: a registration form that asserted them would
		// let the console offer controls the hardware ignores, which is the
		// defect `probeNas` exists to prevent.
		Reachability:         gen.NasReachabilityUnknown,
		MessageAuthenticator: gen.NasMessageAuthenticatorUnknown,
		Capabilities:         gen.Capabilities{WalledGarden: gen.GardenKindNone},
		SecretSet:            true,
		CreatedAt:            ptr(s.clock()),
	}
	s.world.Nas = append([]gen.Nas{n}, s.world.Nas...)
	return n, secret, nil
}

// coaVendorFor mirrors `apistore.coaVendorIn`, and the duplication is the fake's
// whole job: `make diff-api` compares this world against the adapter's, so a rule
// held on one side alone shows up as a difference between two servers rather than
// as the rule it is.
func coaVendorFor(driver gen.NasDriverKind, vendor *gen.CoaVendor) *gen.CoaVendor {
	if driver != gen.NasDriverKindCoa {
		return nil
	}
	return vendor
}

func (s *Store) AmendAccessPoint(_ context.Context, id string, name *string, enabled *bool) (gen.Nas, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.world.Nas {
		n := &s.world.Nas[i]
		if n.Id != id {
			continue
		}
		if name != nil {
			n.Name = *name
		}
		if enabled != nil {
			n.Enabled = *enabled
		}
		return *n, nil
	}
	return gen.Nas{}, api.ErrNotFound
}

func (s *Store) ForgetAccessPoint(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.world.Nas {
		if n.Id == id {
			s.world.Nas = append(s.world.Nas[:i], s.world.Nas[i+1:]...)
			return nil
		}
	}
	return api.ErrNotFound
}

// RecordProbe writes back only what the probe measured.
//
// `not-measured` leaves the stored value alone. An unreachable device does not
// lose what was last known about it, and a probe that could not reach it must
// not overwrite `enforced` with `unknown` — that would read as *this device
// stopped enforcing Message-Authenticator*, which is a BlastRADIUS alarm nobody
// should be woken by.
func (s *Store) RecordProbe(_ context.Context, id string, p gen.NasProbe) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.world.Nas {
		n := &s.world.Nas[i]
		if n.Id != id {
			continue
		}
		n.Reachability = p.Reachability
		n.Capabilities = p.Capabilities
		if p.MessageAuthenticator != gen.NasProbeMessageAuthenticatorNotMeasured {
			n.MessageAuthenticator = gen.NasMessageAuthenticator(p.MessageAuthenticator)
		}
		if p.Accounting != gen.NasProbeAccountingNotMeasured {
			n.AccountingConfigured = ptr(p.Accounting == gen.NasProbeAccountingConfigured)
		}
		if p.Reachability == gen.NasReachabilityOk {
			n.LastSeenAt = ptr(s.clock())
		}
		return nil
	}
	return api.ErrNotFound
}

func (s *Store) StoreRotatedSecret(_ context.Context, id, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.world.Nas {
		n := &s.world.Nas[i]
		if n.Id != id {
			continue
		}
		// The secret itself is not kept: this fake has nowhere safe to put one
		// and no operation may read it back, so keeping it would only create a
		// place for a later handler to read it from.
		_ = secret
		n.SecretSet = true
		n.SecretLastUsedAt = nil
		return nil
	}
	return api.ErrNotFound
}

// ── the driver seam ──────────────────────────────────────────────────────────

func (s *Store) ProbeController(_ context.Context, c gen.Controller) (gen.ControllerProbe, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.world.Capability[c.Id]; ok {
		return p, nil
	}
	return gen.ControllerProbe{
		Reachability: c.Reachability,
		// Empty, never nil. Both are `required` in the contract, and a nil slice
		// marshals to `null` — which is not an empty array and is the difference
		// between *the probe found nothing to warn about* and *this server does not
		// answer that question*. The required-and-possibly-empty shape is the whole
		// reason those fields are not optional.
		Warnings:   []gen.ProbeWarning{},
		Unverified: []string{},
		Checks: []struct {
			Detail *string                         `json:"detail,omitempty"`
			Kind   gen.ControllerProbeChecksKind   `json:"kind"`
			Result gen.ControllerProbeChecksResult `json:"result"`
		}{},
	}, nil
}

func (s *Store) Discover(_ context.Context, c gen.Controller, _, _ string) (gen.DiscoveryResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if d, ok := s.world.Discovery[c.Id]; ok {
		return d, nil
	}
	return gen.DiscoveryResult{SharedSecretId: "sec-" + c.Id}, nil
}

// DriverEvidence — and this fake deliberately answers from `internal/nas` rather
// than from a scripted world.
//
// `docs/rules/testing.md` warns against *a stale second copy of a rule whose
// authority is sitting right there* — **a capability set beside `unifi.go`** is
// one. A `World.DriverEvidence` map would be another, and it would be the worst
// kind: a fake asserting that a Cisco encoding is
// documented, in a test suite whose whole point is that nothing here has met a
// Cisco device.
//
// So it calls the same two functions the adapter calls. What this fake is a fake
// *of* is the network, and there is no network in this answer.
func (s *Store) DriverEvidence(_ context.Context, n gen.Nas) (gen.DriverEvidence, error) {
	out := gen.DriverEvidence{
		Profile:      gen.Resolved,
		Capabilities: []gen.EvidenceFact{},
		Attributes:   []gen.EvidenceFact{},
	}
	coaVendor := ""
	if n.CoaVendor != nil {
		coaVendor = string(*n.CoaVendor)
	}
	vendor, ok := nas.VendorForDriver(string(n.Driver), coaVendor)
	if !ok {
		out.Profile = gen.VendorNotChosen
		return out, nil
	}
	caps, attrs := nas.EvidenceFor(vendor)
	out.Capabilities = evidenceFactsOut(caps)
	out.Attributes = evidenceFactsOut(attrs)
	return out, nil
}

func evidenceFactsOut[M ~map[string]nas.CapabilityFact](m M) []gen.EvidenceFact {
	out := make([]gen.EvidenceFact, 0, len(m))
	for subject, fact := range m {
		out = append(out, gen.EvidenceFact{
			Subject:  subject,
			Evidence: standingOut(fact.Evidence),
			Note:     fact.Note,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out
}

func (s *Store) ProbeAccessPoint(_ context.Context, n gen.Nas) (gen.NasProbe, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.world.NasProbes[n.Id]; ok {
		return p, nil
	}
	// With nothing scripted, the honest answer is that nothing was measured —
	// not a copy of what is already stored dressed up as a fresh reading.
	return gen.NasProbe{
		Reachability:         n.Reachability,
		Capabilities:         n.Capabilities,
		MessageAuthenticator: gen.NasProbeMessageAuthenticatorNotMeasured,
		Accounting:           gen.NasProbeAccountingNotMeasured,
		// See `ProbeController`: empty, never nil.
		Warnings:   []gen.ProbeWarning{},
		Unverified: []string{},
		Checks: []struct {
			Detail *string                  `json:"detail,omitempty"`
			Name   string                   `json:"name"`
			Result gen.NasProbeChecksResult `json:"result"`
		}{},
	}, nil
}

// SecretRotation answers `restart` by default, and the default is the
// conservative one on purpose.
//
// FreeRADIUS 3.2.x reads SQL clients at startup and SIGHUP does not reliably
// reload clients, listeners or virtual servers — `docs/rules/radius.md` is
// explicit about both — so a changed shared secret taking effect is a restart
// until somebody measures otherwise. **The authoritative classification belongs
// to `internal/raddb`, not here**: this is a fake, and a fake that answered
// `sql-only` would be telling an operator that nothing restarts on the strength
// of nobody having looked. Erring towards the louder warning is the only safe
// direction for a value whose whole job is warning.
func (s *Store) SecretRotation(_ context.Context, n gen.Nas) (bool, gen.ChangeClass, error) {
	if s.SecretPush != nil {
		return s.SecretPush(n)
	}
	return n.Capabilities.RotateSecret, gen.ChangeClassRestart, nil
}

func (s *Store) PushSecret(_ context.Context, n gen.Nas, _ string) error {
	if s.SecretPush != nil {
		_, _, err := s.SecretPush(n)
		return err
	}
	return nil
}

func (s *Store) EndSession(_ context.Context, sessionID string) error {
	if s.EndSessionErr != nil {
		if err := s.EndSessionErr(sessionID); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.world.Sessions {
		sess := &s.world.Sessions[i]
		if sess.Id != sessionID {
			continue
		}
		// The driver ended it, so the row says so. `radacct` is FreeRADIUS's to
		// write in the real system; here the fake stands in for the Stop record
		// the NAS would send.
		sess.State = gen.SessionStateEnded
		sess.EndedAt = ptr(s.clock())
		sess.TerminateCause = ptr("Admin-Reset")
		// The voucher no longer points at a session that has ended. Leaving the
		// pointer would make `currentSessionId` mean "was used here once",
		// which is not what any reader of that field expects.
		for j := range s.world.Vouchers {
			v := &s.world.Vouchers[j]
			if v.CurrentSessionId != nil && *v.CurrentSessionId == sessionID {
				v.CurrentSessionId = nil
			}
		}
		return nil
	}
	return api.ErrNotFound
}

// ── vouchers ─────────────────────────────────────────────────────────────────

func (s *Store) Batches(_ context.Context, siteID string, w api.Window) (api.Page[gen.VoucherBatch], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []gen.VoucherBatch
	for _, b := range s.world.Batches {
		if siteID == "" || b.SiteId == siteID {
			out = append(out, b)
		}
	}
	return slicePage(out, w), nil
}

func (s *Store) Batch(_ context.Context, id string) (gen.VoucherBatch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, b := range s.world.Batches {
		if b.Id == id {
			return b, nil
		}
	}
	return gen.VoucherBatch{}, api.ErrNotFound
}

func (s *Store) CreateBatch(_ context.Context, in gen.VoucherBatchCreate, actor string) (gen.VoucherBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b := gen.VoucherBatch{
		Id:         s.id("batch-"),
		Label:      in.Label,
		SiteId:     in.SiteId,
		Count:      in.Count,
		ExpiryMode: in.ExpiryMode,
		CreatedAt:  s.clock(),
		Plan: gen.Plan{
			Name:            in.Plan.Name,
			NameLang:        in.Plan.NameLang,
			DurationMinutes: in.Plan.DurationMinutes,
			DataAllowanceMb: in.Plan.DataAllowanceMb,
			DownKbps:        in.Plan.DownKbps,
			UpKbps:          in.Plan.UpKbps,
			EnforcedBy:      s.enforcementIn(in.SiteId, in.Plan),
		},
	}
	if actor != "" {
		b.CreatedBy = ptr(actor)
	}
	if in.ExpiryMode == gen.FromCreation && in.ValidForHours != nil {
		b.ExpiresAt = ptr(s.clock().Add(time.Duration(*in.ValidForHours) * time.Hour))
	}
	// All five, including the four zeros. `Metric.value` is nullable so that
	// "could not be sampled" and "there are none" stay different answers; these
	// are counts of rows in a table nobody has to sample, so the zeros are
	// known and are stated. An omitted `revoked` would render as blank where
	// the truth is 0.
	b.Counts = &struct {
		Exhausted *int `json:"exhausted,omitempty"`
		Expired   *int `json:"expired,omitempty"`
		InUse     *int `json:"inUse,omitempty"`
		Revoked   *int `json:"revoked,omitempty"`
		Unused    *int `json:"unused,omitempty"`
	}{
		Unused:    ptr(in.Count),
		InUse:     ptr(0),
		Exhausted: ptr(0),
		Expired:   ptr(0),
		Revoked:   ptr(0),
	}

	for i := 0; i < in.Count; i++ {
		code, err := newVoucherCode()
		if err != nil {
			return gen.VoucherBatch{}, err
		}
		s.world.Vouchers = append(s.world.Vouchers, gen.Voucher{
			Id:        b.Id + "-" + strings.TrimSpace(itoa(i)),
			BatchId:   b.Id,
			Code:      code,
			State:     gen.VoucherStateUnused,
			CreatedAt: ptr(s.clock()),
			ExpiresAt: b.ExpiresAt,
		})
	}
	s.world.Batches = append([]gen.VoucherBatch{b}, s.world.Batches...)
	return b, nil
}

// enforcementIn answers *who* will enforce this plan, from the **weakest**
// enabled device in the site rather than the strongest.
//
// A guest who lands on the one access point that cannot apply a rate has an
// unenforced rate, whatever the other eleven do, and a voucher batch is
// redeemable anywhere in the site so it cannot know which one that will be. Same
// rule as `WalledGarden.effectiveKind`.
//
// Three limits, three different answers, and the differences are protocol facts
// rather than preferences:
//
//   - **Duration** is `Session-Timeout` in the Access-Accept. Standard RADIUS,
//     honoured by every driver here, and not gated on any capability — so a
//     duration-only plan is `nas`.
//   - **A data allowance** needs `Capabilities.HardQuota` to be a cap. Without
//     it, it is `foyer-sampled`: `radacct` byte counters advance only on
//     Interim-Update or Stop and `Acct-Interim-Interval` has a 300-second floor,
//     so it is a backstop with overshoot — which is exactly what that value
//     means, and is not the same as nothing enforcing it.
//   - **A rate limit** needs `Capabilities.BandwidthLimits`. There is no sampled
//     equivalent: nothing can retroactively slow a link, so without the
//     capability the figure is `unenforced` and the UI must not present it as a
//     cap.
func (s *Store) enforcementIn(siteID string, p gen.PlanRequest) gen.PlanEnforcedBy {
	wantsRate := p.DownKbps != nil || p.UpKbps != nil
	wantsQuota := p.DataAllowanceMb != nil
	wantsDuration := p.DurationMinutes != nil
	if !wantsRate && !wantsQuota && !wantsDuration {
		return gen.PlanEnforcedByUnenforced
	}

	sawDevice, everyDeviceRates, everyDeviceQuotas := false, true, true
	for _, n := range s.world.Nas {
		if n.SiteId != siteID || !n.Enabled {
			continue
		}
		sawDevice = true
		if !n.Capabilities.BandwidthLimits {
			everyDeviceRates = false
		}
		if !n.Capabilities.HardQuota {
			everyDeviceQuotas = false
		}
	}
	if !sawDevice {
		// A site with no enabled device enforces nothing, and saying `nas`
		// would name a device that is not there.
		return gen.PlanEnforcedByUnenforced
	}
	if wantsRate && !everyDeviceRates {
		return gen.PlanEnforcedByUnenforced
	}
	if wantsQuota && !everyDeviceQuotas {
		return gen.PlanEnforcedByFoyerSampled
	}
	return gen.PlanEnforcedByNas
}

// Vouchers matches `q` against the code only, and normalises the way a guest
// types one: upper-cased with group separators stripped, so a code copied with
// or without dashes finds the same row.
func (s *Store) Vouchers(_ context.Context, q api.VoucherQuery) (api.Page[gen.Voucher], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	inSite := map[string]bool{}
	for _, b := range s.world.Batches {
		if q.SiteID == "" || b.SiteId == q.SiteID {
			inSite[b.Id] = true
		}
	}
	needle := normaliseCode(q.Search)

	var out []gen.Voucher
	for _, v := range s.world.Vouchers {
		if !inSite[v.BatchId] {
			continue
		}
		if q.BatchID != "" && v.BatchId != q.BatchID {
			continue
		}
		if len(q.States) > 0 && !slices.Contains(q.States, v.State) {
			continue
		}
		if needle != "" && !strings.Contains(normaliseCode(v.Code), needle) {
			continue
		}
		out = append(out, v)
	}
	return slicePage(out, q.Window), nil
}

func (s *Store) Voucher(_ context.Context, id string) (gen.Voucher, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.world.Vouchers {
		if v.Id == id {
			return v, nil
		}
	}
	return gen.Voucher{}, api.ErrNotFound
}

func (s *Store) Revoke(_ context.Context, id string) (gen.Voucher, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.world.Vouchers {
		v := &s.world.Vouchers[i]
		if v.Id != id {
			continue
		}
		v.State = gen.VoucherStateRevoked
		v.RevokedAt = ptr(s.clock())
		return *v, nil
	}
	return gen.Voucher{}, api.ErrNotFound
}

// ── sessions ─────────────────────────────────────────────────────────────────

// Sessions matches `q` against the MAC, the username, the address, the device
// name and the accounting session id.
//
// There is deliberately no hostname in that list, and no hostname on `Session`
// at all: DHCP hostnames are client-supplied, frequently absent, and were
// removed from the contract rather than rendered as an identity.
func (s *Store) Sessions(_ context.Context, q api.SessionQuery) (api.Page[gen.Session], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []gen.Session
	for _, sess := range s.world.Sessions {
		if q.SiteID != "" && sess.SiteId != q.SiteID {
			continue
		}
		if len(q.States) > 0 && !slices.Contains(q.States, sess.State) {
			continue
		}
		if q.NasID != "" && sess.NasId != q.NasID {
			continue
		}
		if q.MAC != "" && !strings.Contains(strings.ToLower(sess.CallingStationId), strings.ToLower(q.MAC)) {
			continue
		}
		if q.VoucherID != "" && (sess.VoucherId == nil || *sess.VoucherId != q.VoucherID) {
			continue
		}
		if !matches(q.Search, &sess.CallingStationId, sess.Username, sess.IpAddress, sess.NasName, sess.AcctSessionId) {
			continue
		}
		out = append(out, sess)
	}
	return slicePage(out, q.Window), nil
}

func (s *Store) SessionByID(_ context.Context, id string) (gen.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sess := range s.world.Sessions {
		if sess.Id == id {
			return sess, nil
		}
	}
	return gen.Session{}, api.ErrNotFound
}

// ── accounts ─────────────────────────────────────────────────────────────────

func (s *Store) Accounts(_ context.Context, siteID, search string, w api.Window) (api.Page[gen.Account], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []gen.Account
	for _, a := range s.world.Accounts {
		if siteID != "" && a.SiteId != siteID {
			continue
		}
		if !matches(search, &a.Username, a.DisplayName) {
			continue
		}
		out = append(out, a)
	}
	return slicePage(out, w), nil
}

func (s *Store) CreateAccount(_ context.Context, in gen.AccountCreate) (gen.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.world.Accounts {
		if a.SiteId == in.SiteId && strings.EqualFold(a.Username, in.Username) {
			return gen.Account{}, api.ErrConflict
		}
	}
	a := gen.Account{
		Id:          s.id("acc-"),
		Username:    in.Username,
		DisplayName: in.DisplayName,
		SiteId:      in.SiteId,
		Enabled:     true,
		// argon2id, always. An account reaches `argon2id+nt` only by an explicit
		// opt-in to PEAP-MSCHAPv2, which this operation does not offer and which
		// stores a password-equivalent NT hash in a separate encrypted table.
		CredentialKind: gen.Argon2id,
		CreatedAt:      s.clock(),
	}
	// The 802.1X opt-in at creation: refused, and nothing created, exactly as the
	// store's one transaction refuses.
	if in.EapTtlsPap != nil && *in.EapTtlsPap {
		if !papPasswordOK(in.Password) {
			return gen.Account{}, api.ErrPasswordTooLongFor8021X
		}
		if s.papHolderLocked(in.Username, "") {
			return gen.Account{}, api.ErrPAPUsernameTaken
		}
		a.CredentialKind = with8021X(a.CredentialKind, true)
	}
	// Derived, never copied. `AccountCreate.plan` is a `PlanRequest`, so there is
	// no `enforcedBy` on the way in to copy — and the fake reads the same
	// `enforcementIn` the batch path does, because the differential harness
	// compares this port and `internal/apistore` against the same oracle and a
	// stand-in that answers differently is a divergence about the stand-in.
	if in.Plan != nil {
		a.Plan = &gen.Plan{
			Name:            in.Plan.Name,
			NameLang:        in.Plan.NameLang,
			DurationMinutes: in.Plan.DurationMinutes,
			DataAllowanceMb: in.Plan.DataAllowanceMb,
			DownKbps:        in.Plan.DownKbps,
			UpKbps:          in.Plan.UpKbps,
			EnforcedBy:      s.enforcementIn(in.SiteId, *in.Plan),
		}
	}
	s.world.Accounts = append([]gen.Account{a}, s.world.Accounts...)
	return a, nil
}

func (s *Store) AmendAccount(_ context.Context, id string, in api.AccountAmendment) (gen.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.world.Accounts {
		a := &s.world.Accounts[i]
		if a.Id != id {
			continue
		}
		if in.Password != nil || in.EapTtlsPap != nil {
			has := has8021X(*a)
			want := has
			if in.EapTtlsPap != nil {
				want = *in.EapTtlsPap
			}
			switch {
			case want && in.Password == nil && !has:
				return gen.Account{}, api.ErrPasswordRequiredFor8021X
			case want && in.Password != nil && !papPasswordOK(*in.Password):
				return gen.Account{}, api.ErrPasswordTooLongFor8021X
			case want && !has && s.papHolderLocked(a.Username, a.Id):
				return gen.Account{}, api.ErrPAPUsernameTaken
			}
			a.CredentialKind = with8021X(a.CredentialKind, want)
		}
		if in.DisplayName != nil {
			a.DisplayName = in.DisplayName
		}
		if in.Enabled != nil {
			a.Enabled = *in.Enabled
		}
		return *a, nil
	}
	return gen.Account{}, api.ErrNotFound
}

func (s *Store) DeleteAccount(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.world.Accounts {
		if a.Id == id {
			s.world.Accounts = append(s.world.Accounts[:i], s.world.Accounts[i+1:]...)
			return nil
		}
	}
	return api.ErrNotFound
}

// ── operators ────────────────────────────────────────────────────────────────

func (s *Store) Operators(_ context.Context, search string, w api.Window) (api.Page[gen.Operator], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []gen.Operator
	for _, o := range s.world.Operators {
		if !matches(search, &o.Username, o.DisplayName) {
			continue
		}
		out = append(out, o)
	}
	return slicePage(out, w), nil
}

func (s *Store) OperatorByID(_ context.Context, id string) (gen.Operator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, o := range s.world.Operators {
		if o.Id == id {
			return o, nil
		}
	}
	return gen.Operator{}, api.ErrNotFound
}

func (s *Store) CreateOperator(_ context.Context, in gen.OperatorRequest) (gen.Operator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.world.Operators {
		if strings.EqualFold(o.Username, in.Username) {
			return gen.Operator{}, api.ErrConflict
		}
	}
	o := gen.Operator{
		Id:          s.id("op-"),
		Username:    strings.ToLower(strings.TrimSpace(in.Username)),
		DisplayName: in.DisplayName,
		Role:        in.Role,
		Enabled:     in.Enabled == nil || *in.Enabled,
		CreatedAt:   s.clock(),
	}
	s.world.Operators = append([]gen.Operator{o}, s.world.Operators...)
	s.world.Credentials[o.Id] = in.Password
	return o, nil
}

func (s *Store) AmendOperator(_ context.Context, id string, in gen.OperatorUpdate) (gen.Operator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.world.Operators {
		o := &s.world.Operators[i]
		if o.Id != id {
			continue
		}
		if in.DisplayName != nil {
			o.DisplayName = in.DisplayName
		}
		if in.Role != nil {
			o.Role = *in.Role
		}
		if in.Enabled != nil {
			o.Enabled = *in.Enabled
		}
		return *o, nil
	}
	return gen.Operator{}, api.ErrNotFound
}

func (s *Store) DeleteOperator(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, o := range s.world.Operators {
		if o.Id == id {
			s.world.Operators = append(s.world.Operators[:i], s.world.Operators[i+1:]...)
			delete(s.world.Credentials, id)
			return nil
		}
	}
	return api.ErrNotFound
}

func (s *Store) AdministratorCount(context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, o := range s.world.Operators {
		if o.Role == gen.Administrator && o.Enabled {
			n++
		}
	}
	return n, nil
}

// Authenticate returns one error for every reason.
//
// Unknown username, wrong password and disabled operator are indistinguishable
// from the caller's side, which is what keeps the sign-in form from being a way
// to confirm whether a person has an account here.
func (s *Store) Authenticate(_ context.Context, username, password string) (gen.Operator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, o := range s.world.Operators {
		if !strings.EqualFold(o.Username, username) {
			continue
		}
		if !o.Enabled {
			return gen.Operator{}, api.ErrNotFound
		}
		want, ok := s.world.Credentials[o.Id]
		if !ok || want == "" || want != password {
			return gen.Operator{}, api.ErrNotFound
		}
		return o, nil
	}
	return gen.Operator{}, api.ErrNotFound
}

func (s *Store) ChangePassword(_ context.Context, operatorID, current, next string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	want, ok := s.world.Credentials[operatorID]
	if !ok {
		return api.ErrNotFound
	}
	if want != current {
		return api.ErrConflict
	}
	s.world.Credentials[operatorID] = next
	// The same statement in the real store clears the column; the fake clears it
	// here so the two agree about the one transition that exists.
	delete(s.mustChange, operatorID)
	return nil
}

// MustChangePassword models `operators.must_change_password`.
//
// **The column, not a comparison against a password.** Migration 00017 records
// that *"never changed it"* and *"chose it deliberately"* are different states,
// and a fake that cannot tell them apart is an input the harness cannot be wrong
// about -- `docs/rules/testing.md` says what that costs. This is a
// map because the thing it stands in for is a boolean on a row.
func (s *Store) MustChangePassword(_ context.Context, operatorID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mustChange[operatorID], nil
}

// SetMustChangePassword is the only writer, mirroring
// `foyerctl create-operator --must-change-password` -- there is no API operation
// that sets the flag and there must not be one here either.
func (s *Store) SetMustChangePassword(operatorID string, v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mustChange == nil {
		s.mustChange = map[string]bool{}
	}
	s.mustChange[operatorID] = v
	if !v {
		delete(s.mustChange, operatorID)
	}
}

func (s *Store) RecordSignIn(_ context.Context, operatorID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.world.Operators {
		if s.world.Operators[i].Id == operatorID {
			s.world.Operators[i].LastLoginAt = ptr(at)
			return nil
		}
	}
	return api.ErrNotFound
}

// ── admin sessions ───────────────────────────────────────────────────────────

func (s *Store) CreateAdminSession(_ context.Context, sess api.AdminSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admin[sess.TokenHash] = sess
	return nil
}

func (s *Store) LookupAdminSession(_ context.Context, tokenHash string) (api.AdminSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.admin[tokenHash]
	if !ok {
		return api.AdminSession{}, api.ErrNotFound
	}
	return sess, nil
}

func (s *Store) DeleteAdminSession(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.admin, tokenHash)
	return nil
}

// ── audit ────────────────────────────────────────────────────────────────────

func (s *Store) Audit(_ context.Context, q api.AuditQuery) (api.Page[gen.AuditEntry], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []gen.AuditEntry
	for _, e := range s.world.Audit {
		if q.ActorID != "" && e.ActorId != q.ActorID {
			continue
		}
		// `from` inclusive, `to` exclusive — the contract says so, and a
		// half-open interval is what lets two adjacent windows tile without
		// double-counting an entry.
		if q.From != nil && e.At.Before(*q.From) {
			continue
		}
		if q.To != nil && !e.At.Before(*q.To) {
			continue
		}
		out = append(out, e)
	}
	return slicePage(out, q.Window), nil
}

func (s *Store) Append(_ context.Context, e gen.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Newest first, and append-only: there is no method here that edits or
	// removes an entry, because a log an administrator can edit is not a log.
	s.world.Audit = append([]gen.AuditEntry{e}, s.world.Audit...)
	return nil
}

// ── auth attempts ────────────────────────────────────────────────────────────

func (s *Store) AuthAttempts(_ context.Context, q api.AuthAttemptQuery) (api.Page[gen.AuthAttempt], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []gen.AuthAttempt
	for _, a := range s.world.Attempts {
		if q.SiteID != "" && a.SiteId != q.SiteID {
			continue
		}
		if q.Outcome != nil && a.Outcome != *q.Outcome {
			continue
		}
		if q.Source != nil && a.Source != *q.Source {
			continue
		}
		if !matches(q.Search, a.CallingStationId, a.CalledStationId, a.Username, a.NasId) {
			continue
		}
		out = append(out, a)
	}
	return slicePage(out, q.Window), nil
}

func (s *Store) Coverage(_ context.Context, siteID string) (gen.MacCoverage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c, ok := s.world.Coverage[siteID]; ok {
		return c, nil
	}
	// `unknown`, not `recorded`. Whether the MAC reached `radpostauth` at all
	// depends on the generated post-auth query having populated the station-id
	// columns, and a site nobody has recorded a coverage answer for is a site
	// nobody has checked.
	return gen.MacCoverageUnknown, nil
}

func (s *Store) BlindSpots(_ context.Context, siteID string) ([]gen.AuthBlindSpot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.world.BlindSpots[siteID]; ok {
		return b, nil
	}
	return []gen.AuthBlindSpot{}, nil
}

// ── portal settings ──────────────────────────────────────────────────────────

func (s *Store) PortalSettings(_ context.Context, siteID string) (gen.PortalSettings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.world.Portal[siteID]
	if !ok {
		return gen.PortalSettings{}, api.ErrNotFound
	}
	return p, nil
}

func (s *Store) AmendPortalSettings(_ context.Context, siteID string, in gen.PortalSettingsUpdate) (gen.PortalSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.world.Portal[siteID]
	if !ok {
		return gen.PortalSettings{}, api.ErrNotFound
	}
	if in.VenueName != nil {
		p.VenueName = *in.VenueName
	}
	if in.NetworkName != nil {
		p.NetworkName = in.NetworkName
	}
	if in.Languages != nil {
		p.Languages = *in.Languages
	}
	if in.Methods != nil {
		p.Methods = *in.Methods
	}
	if in.LogoDataUri != nil {
		p.LogoDataUri = in.LogoDataUri
	}
	if in.ClickThroughMinutes != nil {
		p.ClickThroughMinutes = in.ClickThroughMinutes
	}
	if in.Terms != nil && in.Terms.Summary != nil {
		// Replaced wholesale, never merged per language. Terms are legal text a
		// venue authored; a merge would leave one language saying something the
		// venue has since withdrawn.
		p.Terms.Summary = *in.Terms.Summary
	}
	if in.Design != nil {
		// Replaced whole, as the adapter does: a block list merged per element
		// is how two console tabs lose each other's edits.
		p.Design = *in.Design
	}
	s.world.Portal[siteID] = p
	return p, nil
}

func (s *Store) StoreHeroImage(_ context.Context, siteID, filename, contentType string, data []byte) (gen.HeroImage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.world.Portal[siteID]
	if !ok {
		return gen.HeroImage{}, api.ErrNotFound
	}
	img := gen.HeroImage{
		// A same-origin path, never a data URI and never an external origin.
		// The portal's own FQDN is already required to be in the walled garden
		// or the portal cannot serve itself, so a linked asset there costs
		// nothing — and a base64 one costs more than the raw file.
		Url:         "/venue/" + siteID + "/" + filename,
		ContentType: contentType,
		Bytes:       len(data),
		UploadedAt:  ptr(s.clock()),
	}
	p.HeroImage = &img
	s.world.Portal[siteID] = p
	if s.heroBlobs == nil {
		s.heroBlobs = map[string]heroBlob{}
	}
	s.heroBlobs[img.Url] = heroBlob{contentType: contentType, data: append([]byte(nil), data...)}
	return img, nil
}

func (s *Store) DeleteHeroImage(_ context.Context, siteID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.world.Portal[siteID]
	if !ok {
		return api.ErrNotFound
	}
	if p.HeroImage != nil {
		delete(s.heroBlobs, p.HeroImage.Url)
	}
	p.HeroImage = nil
	s.world.Portal[siteID] = p
	return nil
}

// HeroImageBytes serves the stored bytes by site: the public route resolves the
// site from the path, never from a session, so the fake does the same — the
// metadata names the URL, the blob map holds what it points at.
func (s *Store) HeroImageBytes(_ context.Context, siteID string) (string, []byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.world.Portal[siteID]
	if !ok || p.HeroImage == nil {
		return "", nil, api.ErrNotFound
	}
	b, ok := s.heroBlobs[p.HeroImage.Url]
	if !ok {
		return "", nil, api.ErrNotFound
	}
	return b.contentType, append([]byte(nil), b.data...), nil
}

// ── walled garden ────────────────────────────────────────────────────────────

func (s *Store) Garden(_ context.Context, siteID string) (gen.WalledGarden, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.world.Garden[siteID]
	if !ok {
		return gen.WalledGarden{}, api.ErrNotFound
	}
	return g, nil
}

func (s *Store) AddGardenEntry(_ context.Context, siteID string, in gen.GardenEntryCreate) (gen.GardenEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.world.Garden[siteID]
	if !ok {
		return gen.GardenEntry{}, api.ErrNotFound
	}
	for _, e := range g.Entries {
		if strings.EqualFold(e.Value, in.Value) {
			return gen.GardenEntry{}, api.ErrConflict
		}
	}
	e := gen.GardenEntry{
		Id:        s.id("garden-"),
		SiteId:    siteID,
		Kind:      in.Kind,
		Value:     in.Value,
		Note:      in.Note,
		Origin:    gen.GardenEntryOriginOperator,
		CreatedAt: s.clock(),
	}
	g.Entries = append(g.Entries, e)
	s.world.Garden[siteID] = g
	return e, nil
}

func (s *Store) RemoveGardenEntry(_ context.Context, siteID, entryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.world.Garden[siteID]
	if !ok {
		return api.ErrNotFound
	}
	for i, e := range g.Entries {
		if e.Id != entryID {
			continue
		}
		if e.Origin == gen.GardenEntryOriginRequired {
			return api.ErrConflict
		}
		g.Entries = append(g.Entries[:i], g.Entries[i+1:]...)
		s.world.Garden[siteID] = g
		return nil
	}
	return api.ErrNotFound
}

// ── dashboard ────────────────────────────────────────────────────────────────

func (s *Store) Overview(_ context.Context, siteID string) (gen.Overview, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if o, ok := s.world.Overviews[siteID]; ok {
		return o, nil
	}
	// A site nobody supplied an overview for reports **absent**, not zero.
	// `Metric.value` is nullable precisely so that "we could not sample this"
	// and "there are none" are different answers; collapsing them is the defect
	// the whole freshness apparatus exists to prevent.
	absent := gen.Metric{Freshness: gen.Freshness{State: gen.FreshnessStateAbsent}}
	return gen.Overview{ClientsOnline: absent, VouchersRedeemedToday: absent}, nil
}

func (s *Store) Alarms(_ context.Context, siteID string) ([]gen.Alarm, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if a, ok := s.world.Alarms[siteID]; ok {
		return a, nil
	}
	return []gen.Alarm{}, nil
}

// standingOut spells an evidence value the way the contract does. See
// nas.EvidenceLower — a cast compiles and answers with a non-member.
func standingOut(e nas.Evidence) gen.EvidenceStanding {
	return gen.EvidenceStanding(nas.EvidenceLower(e))
}
