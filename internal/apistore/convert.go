package apistore

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/store"
)

// Translation between the store's model and the contract's.
//
// The two disagree in exactly the places they should:
// the store has three nouns where the contract has one read model, and the store
// keeps what PostgreSQL can hold where the contract keeps what a screen
// renders. Everywhere else this file is mechanical, and it is worth
// keeping it that way — a conversion with a decision in it is a decision nobody
// reviews.
//
// Where a conversion *does* carry a decision it is commented, and there are five:
// the octet counters, the freshness of a session, the address on a device, the
// hero image's URL, and the kind of an authentication attempt.

// ── site ─────────────────────────────────────────────────────────────────────

func siteOut(s store.Site) gen.Site {
	return gen.Site{
		Id:          s.ID,
		Name:        s.Name,
		Environment: gen.SiteEnvironment(s.Environment),
	}
}

// ── capabilities ─────────────────────────────────────────────────────────────

func capabilitiesOut(c store.Capabilities) gen.Capabilities {
	garden := c.WalledGarden
	if garden == "" {
		// An unmeasured garden is `none`, never the strongest value. A default
		// of `fqdn-sni` would be the console promising a garden with no enforcer.
		garden = string(gen.GardenKindNone)
	}
	return gen.Capabilities{
		LiveReauthorize: c.LiveReauthorize,
		WalledGarden:    gen.GardenKind(garden),
		HardQuota:       c.HardQuota,
		BandwidthLimits: c.BandwidthLimits,
		DynamicVlan:     c.DynamicVLAN,
		RotateSecret:    c.RotateSecret,
		CanDeauthGuest:  c.CanDeauthGuest,
	}
}

// gardenKindOut translates the *driver's* garden vocabulary into the contract's.
//
// The two enums are three-valued for the same reason and agree on exactly one
// word. `nas.WalledGardenFQDN` is `fqdn` where `gen.GardenKindFqdnSni` is
// `fqdn-sni`; `nas.WalledGardenIP` is `ip` where `gen.GardenKindIpRange` is
// `ip-range`; only `none` is spelled the same in both. A bare conversion in
// `probeNas` would therefore emit a value failing `GardenKind.Valid()` for two of
// three inputs.
//
// Nothing would catch it: the only shipped driver returns the one value the two
// enums share, and a fake that returned `fqdn` — the wrong spelling of a
// capability no deployment has — would go unasserted. Both producers would agree,
// and the agreement would be the bug.
//
// An unrecognised value becomes `none`, never the strongest: a garden the
// contract cannot name is not a garden the console may promise.
func gardenKindOut(g nas.WalledGardenType) gen.GardenKind {
	switch g {
	case nas.WalledGardenFQDN:
		return gen.GardenKindFqdnSni
	case nas.WalledGardenIP:
		return gen.GardenKindIpRange
	default:
		return gen.GardenKindNone
	}
}

func capabilitiesIn(c gen.Capabilities) store.Capabilities {
	return store.Capabilities{
		LiveReauthorize: c.LiveReauthorize,
		WalledGarden:    string(c.WalledGarden),
		HardQuota:       c.HardQuota,
		BandwidthLimits: c.BandwidthLimits,
		DynamicVLAN:     c.DynamicVlan,
		RotateSecret:    c.RotateSecret,
		CanDeauthGuest:  c.CanDeauthGuest,
	}
}

// ── controller ───────────────────────────────────────────────────────────────

func controllerOut(c store.Controller) gen.Controller {
	sites := make([]struct {
		BoundSiteId *string `json:"boundSiteId,omitempty"`
		Id          string  `json:"id"`
		Name        string  `json:"name"`
	}, 0, len(c.VendorSites))
	for _, vs := range c.VendorSites {
		sites = append(sites, struct {
			BoundSiteId *string `json:"boundSiteId,omitempty"`
			Id          string  `json:"id"`
			Name        string  `json:"name"`
		}{BoundSiteId: vs.BoundSiteID, Id: vs.ID, Name: vs.Name})
	}
	return gen.Controller{
		Id:                     c.ID,
		Name:                   c.Name,
		Vendor:                 gen.ControllerVendor(c.Vendor),
		BaseUrl:                c.BaseURL,
		AuthMode:               gen.ControllerAuthMode(c.AuthMode),
		TlsMode:                gen.TlsMode(c.TLSMode),
		ControllerId:           c.VendorControllerID,
		CredentialSet:          c.CredentialSet,
		CertificateFingerprint: c.CertificateFingerprint,
		Reachability:           gen.NasReachability(c.Reachability),
		Version:                c.Version,
		VersionSupported:       c.VersionSupported,
		VendorSites:            sites,
		LastContactAt:          c.LastContactAt,
		CreatedAt:              timePtr(c.CreatedAt),
	}
}

// ── nas ──────────────────────────────────────────────────────────────────────

// nasOut renders the device read model.
//
// `Nas.ipAddress` is the one field with a choice in it. A device discovered from a
// controller has its own address; a `clients.conf` row with no access point behind
// it — a MikroTik that is its own NAS — has only the client address, and that *is*
// the device's address. Preferring the access point's and falling back to the
// client's is what makes both cases render the same field honestly, rather than
// leaving a MikroTik with a blank address on the screen that lists it.
func nasOut(n store.Nas) gen.Nas {
	via := gen.NasRadiusVia(n.RadiusVia)
	out := gen.Nas{
		Id:                   n.ID,
		Name:                 n.Name,
		SiteId:               n.SiteID,
		Description:          n.Description,
		Driver:               gen.NasDriverKind(n.Driver),
		Source:               gen.NasSource(n.Source),
		ControllerId:         n.ControllerID,
		SharedSecretId:       n.SharedSecretID,
		NasIdentifier:        n.NASIdentifier,
		CoaPort:              n.CoAPort,
		CoaVendor:            coaVendorOut(n.CoAVendor),
		MessageAuthenticator: gen.NasMessageAuthenticator(n.MessageAuthState),
		AccountingConfigured: n.AccountingConfigured,
		Reachability:         gen.NasReachability(n.Reachability),
		Capabilities:         capabilitiesOut(n.Capabilities),
		Enabled:              n.Enabled,
		Mac:                  n.MAC,
		RadiusVia:            &via,
		Model:                n.Model,
		Firmware:             n.Firmware,
		IpAddress:            n.IPAddress,
		LastSeenAt:           n.LastSeenAt,
		SecretSet:            n.SecretSet,
		SecretLastUsedAt:     n.SecretUsed,
		CreatedAt:            timePtr(n.CreatedAt),
	}
	if out.IpAddress == nil && n.Address.IsValid() {
		out.IpAddress = ptr(addressText(n.Address))
	}
	return out
}

// coaVendorOut renders `radius_clients.coa_vendor`, and drops a value the contract
// does not name.
//
// **Dropped rather than coerced**, the same rule `warningsOut` follows for a probe
// warning kind: the console has copy for each vendor it knows, and rendering an
// unrecognised one as `cisco` would tell an operator a fact about the wrong vendor.
// A dropped value reads as *nobody has said*, which is what the console shows for a
// `coa` device that cannot be driven -- and is true of a value nothing can build a
// driver from.
func coaVendorOut(v *string) *gen.CoaVendor {
	if v == nil {
		return nil
	}
	kind := gen.CoaVendor(*v)
	if !kind.Valid() {
		return nil
	}
	return &kind
}

// addressText renders a client address the way an operator typed it: a single host
// without the redundant `/32`, a range with its prefix length.
func addressText(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}

// ── vouchers ─────────────────────────────────────────────────────────────────

func planOut(p store.Plan) gen.Plan {
	out := gen.Plan{
		Name:            p.Name,
		DurationMinutes: p.DurationMinutes,
		DataAllowanceMb: p.DataAllowanceMB,
		DownKbps:        p.DownKbps,
		UpKbps:          p.UpKbps,
		EnforcedBy:      gen.PlanEnforcedBy(p.EnforcedBy),
	}
	if p.NameLang != nil {
		out.NameLang = ptr(gen.Language(*p.NameLang))
	}
	if out.EnforcedBy == "" {
		out.EnforcedBy = gen.PlanEnforcedByUnenforced
	}
	return out
}

// planIn writes down what the operator asked for, plus the enforcement answer the
// **server** derived.
//
// It takes a `gen.PlanRequest` — the type with no `enforcedBy` member at all — and
// takes the derived value as a separate argument rather than reaching for the site
// itself. Both halves of that are deliberate. The parameter type is what makes
// "the client does not send this" a compile-time fact rather than a convention, and
// leaving the derivation to the caller keeps `enforcementIn` the single authority:
// this function only records what that one said.
//
// Reading a `gen.Plan` and copying `p.EnforcedBy` through would have
// `createAccount` store a client's claim about hardware it had never looked at
// while `createVoucherBatch`, three files away, derived the same value.
func planIn(p gen.PlanRequest, enforced gen.PlanEnforcedBy) store.Plan {
	out := store.Plan{
		Name:            p.Name,
		DurationMinutes: p.DurationMinutes,
		DataAllowanceMB: p.DataAllowanceMb,
		DownKbps:        p.DownKbps,
		UpKbps:          p.UpKbps,
		EnforcedBy:      string(enforced),
	}
	if p.NameLang != nil {
		out.NameLang = ptr(string(*p.NameLang))
	}
	return out
}

func batchOut(b store.VoucherBatch) gen.VoucherBatch {
	return gen.VoucherBatch{
		Id:         b.ID,
		SiteId:     b.SiteID,
		Label:      b.Label,
		Count:      b.Count,
		CreatedBy:  b.CreatedBy,
		Plan:       planOut(b.Plan),
		ExpiryMode: gen.ExpiryMode(b.ExpiryMode),
		ExpiresAt:  b.ExpiresAt,
		CreatedAt:  b.CreatedAt,
		// All five, including the zeros. `Metric.value` is nullable so that
		// "could not be sampled" and "there are none" stay different answers;
		// these are counts of rows in a table nobody has to sample, so the
		// zeros are known and are stated.
		Counts: &struct {
			Exhausted *int `json:"exhausted,omitempty"`
			Expired   *int `json:"expired,omitempty"`
			InUse     *int `json:"inUse,omitempty"`
			Revoked   *int `json:"revoked,omitempty"`
			Unused    *int `json:"unused,omitempty"`
		}{
			Unused:    ptr(b.Counts.Unused),
			InUse:     ptr(b.Counts.InUse),
			Exhausted: ptr(b.Counts.Exhausted),
			Expired:   ptr(b.Counts.Expired),
			Revoked:   ptr(b.Counts.Revoked),
		},
	}
}

func voucherOut(v store.Voucher) gen.Voucher {
	return gen.Voucher{
		Id:               v.ID,
		BatchId:          v.BatchID,
		Code:             v.Code,
		State:            gen.VoucherState(v.State),
		FirstUsedAt:      v.FirstUsedAt,
		ExpiresAt:        v.ExpiresAt,
		RevokedAt:        v.RevokedAt,
		CurrentSessionId: v.CurrentSessionID,
		CreatedAt:        timePtr(v.CreatedAt),
	}
}

// ── sessions ─────────────────────────────────────────────────────────────────

// twoPow32 is the width of the RADIUS octet counters.
const twoPow32 = int64(4294967296)

// sessionOut renders one accounting row, and carries the two decisions that make
// a byte figure honest.
//
// **The counters.** `store.Session.InputOctets` is already the reconstructed
// 64-bit total: the generated accounting query combines `Acct-Input-Octets` with
// `Acct-Input-Gigawords` (RFC 2866 §5.3–5.4, RFC 2869 §5.1–5.2). The contract
// wants both halves and keeps them apart on purpose — `inputOctets` is what the
// NAS last reported and is the evidence, `inputOctetsTotal` is the reconstruction
// and is a lower bound once a wrap has been seen. So the reported counter is
// recovered as the low word of the total, which is exactly what the NAS sent, and
// the total is passed through unchanged. Never subtract two of these without
// reading `countersWrapped` first.
//
// **The freshness.** `radacct` byte counters advance on Interim-Update or Stop and
// nothing else, and `Acct-Interim-Interval` has a widely-implemented 300-second
// floor. A session with no counters at all has never been sampled and says
// `absent`; one whose last update is older than twice its own interval says
// `stale`. A bare figure with no freshness beside it is the console asserting
// something the network never told it.
func sessionOut(s store.Session) gen.Session {
	out := gen.Session{
		Id:               s.ID,
		SiteId:           s.SiteID,
		NasId:            s.NasID,
		NasName:          ptr(s.NasName),
		State:            gen.SessionState(s.State),
		CallingStationId: s.CallingStationID,
		Username:         s.Username,
		IpAddress:        s.IPAddress,
		ApName:           s.APName,
		Ssid:             s.SSID,
		StartedAt:        s.StartedAt,
		EndedAt:          s.EndedAt,
		LastUpdateAt:     s.LastUpdateAt,
		TerminateCause:   s.TerminateCause,
		CountersWrapped:  s.CountersWrapped,
		VoucherId:        s.VoucherID,
		MacIsRandomised:  ptr(s.MACIsRandomised),
		Freshness:        sessionFreshness(s),
	}
	if s.AcctSessionID != "" {
		out.AcctSessionId = ptr(s.AcctSessionID)
	}
	if s.IdentityKind != "" {
		out.IdentityKind = ptr(gen.SessionIdentityKind(s.IdentityKind))
	}
	if s.SessionSeconds != nil {
		out.SessionSeconds = ptr(int(*s.SessionSeconds))
	}
	out.InputOctets, out.InputOctetsTotal = octets(s.InputOctets)
	out.OutputOctets, out.OutputOctetsTotal = octets(s.OutputOctets)
	return out
}

// octets splits a reconstructed total into the counter the NAS reported and the
// total itself. See sessionOut for why both are carried.
func octets(total *int64) (reported, reconstructed *int) {
	if total == nil {
		return nil, nil
	}
	return ptr(int(*total % twoPow32)), ptr(int(*total))
}

// interimFloor is the 300-second floor on `Acct-Interim-Interval`. A NAS claiming
// less is not believed, because the floor is what implementations actually apply.
const interimFloor = 300

func sessionFreshness(s store.Session) gen.Freshness {
	interval := interimFloor
	if s.AcctInterval != nil && int(*s.AcctInterval) > interval {
		interval = int(*s.AcctInterval)
	}
	// Nothing has ever been counted for this session. `absent`, not zero — the
	// device with no accounting profile is a fixture precisely because a console
	// that renders "0 bytes" for it is claiming a measurement.
	if s.InputOctets == nil && s.OutputOctets == nil {
		return gen.Freshness{State: gen.FreshnessStateAbsent, IntervalSeconds: ptr(interval)}
	}

	sampledAt := s.LastUpdateAt
	if s.EndedAt != nil {
		// A Stop record is the last thing this session will ever report, so it
		// is the sample.
		sampledAt = s.EndedAt
	}
	if sampledAt == nil {
		sampledAt = ptr(s.StartedAt)
	}
	state := gen.FreshnessStateSampled
	if time.Since(*sampledAt) > time.Duration(2*interval)*time.Second {
		state = gen.FreshnessStateStale
	}
	return gen.Freshness{State: state, SampledAt: sampledAt, IntervalSeconds: ptr(interval)}
}

// ── accounts and operators ───────────────────────────────────────────────────

func accountOut(a store.Account) gen.Account {
	return gen.Account{
		Id:             a.ID,
		SiteId:         a.SiteID,
		Username:       a.Username,
		DisplayName:    a.DisplayName,
		Enabled:        a.Enabled,
		CredentialKind: gen.AccountCredentialKind(a.CredentialKind),
		Plan:           ptr(planOut(a.Plan)),
		LastLoginAt:    a.LastLoginAt,
		CreatedAt:      a.CreatedAt,
	}
}

func operatorOut(o store.Operator) gen.Operator {
	return gen.Operator{
		Id:          o.ID,
		Username:    o.Username,
		DisplayName: o.DisplayName,
		Role:        gen.OperatorRole(o.Role),
		Enabled:     o.Enabled,
		LastLoginAt: o.LastLoginAt,
		CreatedAt:   o.CreatedAt,
	}
}

// ── audit ────────────────────────────────────────────────────────────────────

func auditOut(e store.AuditEntry) gen.AuditEntry {
	out := gen.AuditEntry{
		Id:            e.ID,
		At:            e.At,
		ActorId:       e.ActorID,
		ActorUsername: e.ActorUsername,
		Kind:          gen.AuditKind(e.Kind),
		SiteId:        e.SiteID,
	}
	if len(e.Params) > 0 {
		p := map[string]any(e.Params)
		out.Params = &p
	}
	if e.SubjectID != "" || e.SubjectKind != "" {
		out.Subject = &gen.AuditSubject{
			Kind: gen.AuditSubjectKind(e.SubjectKind),
			Id:   e.SubjectID,
			Name: e.SubjectName,
		}
	}
	return out
}

func auditIn(e gen.AuditEntry) store.AuditEntry {
	out := store.AuditEntry{
		ID:            e.Id,
		At:            e.At,
		ActorID:       e.ActorId,
		ActorUsername: e.ActorUsername,
		Kind:          string(e.Kind),
		SiteID:        e.SiteId,
		// `subject_kind` is NOT NULL, and an entry with no subject is an entry
		// nothing can be filtered by. `site` is the widest true answer.
		SubjectKind: string(gen.AuditSubjectKindSite),
	}
	if e.Params != nil {
		out.Params = map[string]any(*e.Params)
	}
	if e.Subject != nil {
		out.SubjectKind = string(e.Subject.Kind)
		out.SubjectID = e.Subject.Id
		out.SubjectName = e.Subject.Name
	}
	return out
}

// ── walled garden ────────────────────────────────────────────────────────────

func gardenEntryOut(e store.GardenEntry) gen.GardenEntry {
	return gen.GardenEntry{
		Id:        e.ID,
		SiteId:    e.SiteID,
		Kind:      gen.GardenEntryKind(e.Kind),
		Value:     e.Value,
		Note:      e.Note,
		Origin:    gen.GardenEntryOrigin(e.Origin),
		CreatedAt: e.CreatedAt,
	}
}

// ── portal ───────────────────────────────────────────────────────────────────

func portalOut(p store.PortalSettings, hero *store.HeroImage) (gen.PortalSettings, error) {
	langs := make([]gen.Language, 0, len(p.Languages))
	for _, l := range p.Languages {
		langs = append(langs, gen.Language(l))
	}
	methods := make([]gen.PortalMethod, 0, len(p.Methods))
	for _, m := range p.Methods {
		methods = append(methods, gen.PortalMethod(m))
	}
	out := gen.PortalSettings{
		SiteId:              p.SiteID,
		PortalUrl:           p.PortalURL,
		VenueName:           p.VenueName,
		NetworkName:         p.NetworkName,
		LogoDataUri:         p.LogoDataURI,
		Languages:           langs,
		Methods:             methods,
		ClickThroughMinutes: p.ClickThroughMinutes,
	}
	summary := p.TermsSummary
	if summary == nil {
		summary = map[string]string{}
	}
	out.Terms.Summary = summary
	design, err := designOut(p.Design)
	if err != nil {
		return gen.PortalSettings{}, err
	}
	out.Design = design
	if hero != nil {
		out.HeroImage = ptr(heroOut(*hero))
	}
	return out, nil
}

// designOut decodes the stored design. The column holds the contract's
// `PortalDesign` verbatim — `designIn` wrote it from the generated type after the
// API validated it — so this is the same type read back rather than a mapping.
//
// A row that cannot be decoded is an error, not an empty design: answering "no
// blocks" for a venue whose page is corrupt would let the next save overwrite
// it with nothing and call that a success. An absent value (a caller that never
// read the column) is the empty design.
func designOut(raw []byte) (gen.PortalDesign, error) {
	var d gen.PortalDesign
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &d); err != nil {
			return gen.PortalDesign{}, fmt.Errorf("apistore: portal_settings.design is not a PortalDesign: %w", err)
		}
	}
	if d.Blocks == nil {
		d.Blocks = []gen.PortalBlock{}
	}
	return d, nil
}

// designIn encodes a design for the column. `blocks` is written as an array even
// when the caller's slice is nil, so the stored value is always wire-shaped.
func designIn(d gen.PortalDesign) ([]byte, error) {
	if d.Blocks == nil {
		d.Blocks = []gen.PortalBlock{}
	}
	return json.Marshal(d)
}

// heroOut renders the venue photograph's served URL.
//
// **A same-origin path, never a data URI and never an external origin.** The
// portal's own FQDN is already required to be in the walled garden or the portal
// cannot serve itself, so a linked asset there costs nothing — and a base64 one
// costs a third more than the file.
//
// The filename is derived from the content type rather than kept, because
// `portal_hero_images` has no filename column: one row per site, replaced on
// upload. That is a divergence from `internal/api/memory`, which builds the URL
// from the submitted filename, and it is reported rather than worked around by
// adding a column from this side of the seam.
func heroOut(h store.HeroImage) gen.HeroImage {
	return gen.HeroImage{
		Url:         "/venue/" + h.SiteID + "/hero" + heroExtension(h.ContentType),
		ContentType: h.ContentType,
		Bytes:       h.Bytes,
		UploadedAt:  timePtr(h.UploadedAt),
	}
}

func heroExtension(contentType string) string {
	switch contentType {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	default:
		return ".jpg"
	}
}

// ── authentication attempts ──────────────────────────────────────────────────

// authAttemptOut renders one row of either authentication stream.
//
// **`kind` is the one decision here, and it is a refusal to invent.** A
// `radpostauth` row records `Access-Accept` or `Access-Reject` and nothing else, so
// the only two kinds it can honestly claim are `radius-accept` and `radius-reject`.
// The contract says the same in place: there is deliberately no
// `radius-reject-because-*`, because inventing a reason would be the API asserting
// something it did not observe.
//
// A `driver` row is the other side of that rule. It records exactly what Foyer's own
// call to the controller did, so it *can* say why — `authorize-failed` with a
// `failure` and a `status` in its params, which is the one kind an operator is most
// likely to be looking for and the one nothing wrote for longest.
//
// The device and the site come off the row: for a RADIUS row the repository resolves
// them from `Called-Station-Id`, and both are absent for an attempt on an access
// point nothing recognises. `siteId` is a plain string in the contract, so an
// unresolved row carries the empty one — which only reaches a client that asked for
// no site, since a scoped query excludes those rows in SQL.
func authAttemptOut(a store.AuthAttempt) gen.AuthAttempt {
	siteID := ""
	if a.SiteID != nil {
		siteID = *a.SiteID
	}
	out := gen.AuthAttempt{
		Id:               authAttemptID(a),
		At:               a.At,
		Outcome:          gen.AuthOutcome(a.Outcome),
		Kind:             authEventKind(a),
		Source:           gen.AuthSource(a.Source),
		CallingStationId: a.CallingStationID,
		CalledStationId:  a.CalledStationID,
		NasId:            a.NasID,
		SiteId:           siteID,
		VoucherId:        a.VoucherID,
	}
	if a.Username != "" {
		out.Username = ptr(a.Username)
	}
	if params := authAttemptParams(a); len(params) > 0 {
		out.Params = &params
	}
	return out
}

// authAttemptID keeps the two streams apart in one id space.
//
// `AuthAttempt.id` is one string in the contract and the two tables have independent
// sequences, so `radpostauth` row 7 and `authorization_events` row 7 would otherwise
// be the same identifier for two different events — and a console keying rows on it
// would render one and drop the other. The stream is the prefix; the row key follows.
func authAttemptID(a store.AuthAttempt) string {
	return a.Source + "-" + formatInt64(a.ID)
}

// authEventKind is what happened, as a kind the client renders its own copy from.
func authEventKind(a store.AuthAttempt) gen.AuthEventKind {
	if a.Source == string(gen.Driver) {
		if a.Outcome == string(gen.Accepted) {
			// The venue decided and the network was opened. `grant-created` is the
			// contract's kind for that, and it is the only honest one available:
			// the grant row is what this event is about.
			return gen.AuthEventKindGrantCreated
		}
		return gen.AuthEventKindAuthorizeFailed
	}
	if a.Outcome == string(gen.Accepted) {
		return gen.AuthEventKindRadiusAccept
	}
	return gen.AuthEventKindRadiusReject
}

// authAttemptParams carries the structured values the client interpolates.
//
// **Never the controller's response body and never the URL it was reached at.** The
// store has no column either could arrive in, so this cannot leak one by accident;
// it is stated here as well because this is the function somebody extends.
//
// `attempts` is carried whenever the controller was asked more than once — on a
// success as well as a failure, and that half is the deliberate one: an operator
// reading *the network was opened* has no other way to tell a healthy controller
// from one that answered only on the third ask. A single attempt is omitted because
// it is the ordinary case and carries no information.
func authAttemptParams(a store.AuthAttempt) map[string]any {
	params := map[string]any{}
	if a.Failure != nil {
		params["failure"] = *a.Failure
	}
	if a.Status != nil {
		params["status"] = *a.Status
	}
	if a.Attempts != nil && *a.Attempts > 1 {
		params["attempts"] = *a.Attempts
	}
	return params
}

func formatInt64(v int64) string { return strconv.FormatInt(v, 10) }
