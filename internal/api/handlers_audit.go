package api

import (
	"context"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The audit log, and the one function every mutating handler calls.
//
// **Read-only forever, and for a different reason than `radacct`.** `radacct` is
// read-only because FreeRADIUS owns it; this is read-only because a log an
// administrator can edit is not a log. There is no create, no update and no
// delete operation in the contract and there must never be one.
//
// **Every entry is a kind plus params, never a sentence.** A log is the worst
// possible place to compose text in the server's language: it is read long after
// the fact, by somebody who was not there, and a confidently phrased sentence in
// the wrong language is indistinguishable from a correct one. `AuditKind` is the
// closed list, and an action with no member of it is left unrecorded rather than
// filed under an approximate one.

func (s *Server) ListAudit(ctx context.Context, request gen.ListAuditRequestObject) (gen.ListAuditResponseObject, error) {
	if s.ports.Audit == nil {
		return nil, errNotImplemented
	}
	p := request.Params
	page, err := s.ports.Audit.Audit(ctx, AuditQuery{
		ActorID: text(p.ActorId),
		From:    p.From,
		To:      p.To,
		Window:  windowFrom(p.Cursor, p.Limit),
	})
	if err != nil {
		return nil, err
	}
	items := page.Items
	if items == nil {
		items = []AuditEntry{}
	}
	return gen.ListAudit200JSONResponse{Items: items, NextCursor: page.NextCursor}, nil
}

// auditScope says whether a kind of change belongs to a venue or to the whole
// deployment.
//
// **It exists because `null` would otherwise answer two questions at once.** Were
// every entry this package wrote site-less, `site-created` included, a NULL in
// `audit_log.site_id` would mean *this change had no venue* and *nobody recorded
// the venue* indistinguishably. `docs/rules/database.md` calls a nullable column with no rule
// beside it exactly this shape.
//
// The rule is per **kind**, not per call site, and that is the whole point: the
// reader of a log written months ago has the kind and nothing else, so the kind has
// to be what says how to read the value. A kind declared deployment-wide carries no
// site *by construction* — `recordIn` refuses to attach one — so a null on
// `operator-created` can only ever mean what the contract says it means.
type auditScope uint8

const (
	// scopeUndecided is the zero value, so a kind absent from the table below
	// takes it. Nothing chooses it deliberately; `TestEveryAuditKindDeclaresItsScope`
	// reads the enum out of the embedded contract and fails on the first member
	// that has one.
	scopeUndecided auditScope = iota
	scopeSite
	scopeDeployment
)

// auditScopes is that rule, and it agrees with `web/mock` kind for kind — the mock
// has set `siteId` on every entry since the member existed and is the oracle here.
//
// The three families it separates:
//
//   - **A venue's own changes.** Devices, vouchers, the walled garden, the portal
//     screen, and the venue's creation. Each of these is something an operator did
//     to one site and would look for under that site.
//   - **A controller's registration, amendment and deletion are the deployment's**,
//     not a venue's, and this is the row most likely to be re-argued. Migration
//     `00011` made the vendor-site binding **per controller**: one controller can
//     feed several venues, so naming one of them on the row that registered it
//     would be inventing a fact. Its *discovery run* is the opposite — the request
//     names the site the run adopts into, and that is a venue's afternoon.
//   - **Operators are the deployment's.** There is no per-site role in this product
//     (`ROLE_PERMISSIONS` in `web/contract/src/index.ts` is not indexed by site), so
//     an operator created while looking at one venue is not that venue's operator.
//     Stamping the caller's current site here is the careless repair this table
//     exists to make impossible.
var auditScopes = map[gen.AuditKind]auditScope{
	gen.AuditKindSiteCreated:            scopeSite,
	gen.AuditKindNasRegistered:          scopeSite,
	gen.AuditKindNasDeleted:             scopeSite,
	gen.AuditKindNasSecretRotated:       scopeSite,
	gen.AuditKindControllerDiscoveryRun: scopeSite,
	gen.AuditKindGardenEntryAdded:       scopeSite,
	gen.AuditKindGardenEntryRemoved:     scopeSite,
	gen.AuditKindPortalSettingsChanged:  scopeSite,
	gen.AuditKindPortalTermsChanged:     scopeSite,
	gen.AuditKindVoucherBatchCreated:    scopeSite,
	gen.AuditKindVoucherRevoked:         scopeSite,
	// The venue whose rows left. An export is always of one site's data, and the
	// question the entry answers — *who took this venue's guests home, and when* —
	// is a venue's question.
	gen.AuditKindDataExported:        scopeSite,
	gen.AuditKindSessionDisconnected: scopeSite,
	// Guest data: a venue's own sign-in form, and a consent one of its guests gave.
	gen.AuditKindGuestDataSettingsChanged:  scopeSite,
	gen.AuditKindMarketingConsentWithdrawn: scopeSite,
	// A venue's calendar is the venue's.
	gen.AuditKindSiteTimeZoneChanged: scopeSite,

	gen.AuditKindControllerRegistered:    scopeDeployment,
	gen.AuditKindControllerUpdated:       scopeDeployment,
	gen.AuditKindControllerDeleted:       scopeDeployment,
	gen.AuditKindOperatorCreated:         scopeDeployment,
	gen.AuditKindOperatorUpdated:         scopeDeployment,
	gen.AuditKindOperatorDeleted:         scopeDeployment,
	gen.AuditKindOperatorPasswordChanged: scopeDeployment,
	// A retention period and an erasure apply to every venue's data.
	gen.AuditKindRetentionPolicyChanged: scopeDeployment,
	gen.AuditKindDataSubjectErased:      scopeDeployment,
	// The `network` tag: one portal address, one certificate, one FreeRADIUS and
	// one client table serve every venue of the deployment.
	gen.AuditKindNetworkSettingsChanged:     scopeDeployment,
	gen.AuditKindPortalCertificateChanged:   scopeDeployment,
	gen.AuditKindRadiusClientsSynced:        scopeDeployment,
	gen.AuditKindRadiusConfigurationApplied: scopeDeployment,
	// §9's global cap bounds every venue at once.
	gen.AuditKindPhoneSpendCapChanged: scopeDeployment,
	// One schedule for the whole control plane; an account is one venue's.
	gen.AuditKindMaintenanceSettingsChanged: scopeDeployment,
	gen.AuditKindAccountCredentialsChanged:  scopeSite,
}

// record appends one audit entry for a change that belongs to no venue.
//
// It swallows the error deliberately and that is a judgement worth stating: the
// action has already happened, so failing the response would tell the operator
// their change did not land when it did. A logging failure is an operational
// fault to alarm on, not a reason to lie about a completed write.
//
// **Not for an irreversible change.** An erasure and a retention period are
// written with `auditEntry` inside the change's own transaction, so a failure to
// log is a failure to change; an export writes its entry before a byte
// leaves. A new irreversible operation takes one of those two shapes, not this.
//
// `actorUsername` is denormalised at the moment the entry is written, which is
// why `Operator.username` is immutable: renaming an operator would otherwise
// leave one person under two names across a log that is read chronologically.
func (s *Server) record(ctx context.Context, who *caller, kind gen.AuditKind, subject gen.AuditSubject, params map[string]any) {
	s.recordIn(ctx, who, "", kind, subject, params)
}

// recordIn appends one audit entry for a change that happened in a venue.
//
// Two functions rather than one argument every call site has to spell, because the
// site is not optional information the caller may omit — it is the shape of the
// kind. A handler naming the wrong one of these is caught by
// `TestAnAuditEntryCarriesTheVenueItsChangeBelongsTo` in `internal/apistore`, which
// drives real operations in two venues and reads the log back under one venue's
// scope.
//
// **The table is consulted, never trusted to the caller.** A `siteID` passed for a
// deployment-wide kind is dropped rather than written: the answer for that kind is
// null in every deployment, and one handler quietly disagreeing is how a log stops
// being readable by rule. The reverse — a site-scoped kind whose site could not be
// resolved — still writes the entry, because losing the record of a change that
// happened is worse than a row a test can find; and it is a defect in the caller
// rather than a value a reader has to interpret, which is the distinction the whole
// table exists to keep.
func (s *Server) recordIn(ctx context.Context, who *caller, siteID string, kind gen.AuditKind, subject gen.AuditSubject, params map[string]any) {
	if s.ports.Audit == nil || who == nil {
		return
	}
	_ = s.ports.Audit.Append(ctx, s.auditEntry(who, siteID, kind, subject, params))
}

// auditEntry is the entry recordIn writes, for a caller that writes it itself —
// fail-closed, inside the transaction of an irreversible change (the privacy
// operations).
func (s *Server) auditEntry(who *caller, siteID string, kind gen.AuditKind, subject gen.AuditSubject, params map[string]any) AuditEntry {
	e := AuditEntry{
		Id:            s.newID("aud-"),
		At:            s.now(),
		ActorId:       who.Operator.Id,
		ActorUsername: who.Operator.Username,
		Kind:          kind,
		Subject:       &subject,
	}
	if siteID != "" && auditScopes[kind] == scopeSite {
		e.SiteId = &siteID
	}
	if len(params) > 0 {
		p := map[string]any{}
		for k, v := range params {
			p[k] = v
		}
		e.Params = &p
	}
	return e
}

// ── diagnostics ──────────────────────────────────────────────────────────────

// ListAuthAttempts answers "why can't this guest get online?".
//
// The two members beside `items` are required by the contract and are the whole
// point of the operation: an empty result set means *we have no record*, and a
// console that renders that as *nothing went wrong* is the exact failure this
// operation exists to prevent. `blindSpots` names the ways of failing to get
// online that leave no row here at all, and it travels with the result rather
// than living in the client's copy so that a future backend can correct it.
func (s *Server) ListAuthAttempts(ctx context.Context, request gen.ListAuthAttemptsRequestObject) (gen.ListAuthAttemptsResponseObject, error) {
	if s.ports.AuthAttempts == nil {
		return nil, errNotImplemented
	}
	p := request.Params
	site := string(p.SiteId)

	page, err := s.ports.AuthAttempts.AuthAttempts(ctx, AuthAttemptQuery{
		SiteID:  site,
		Search:  text(p.Q),
		Outcome: p.Outcome,
		Source:  p.Source,
		Window:  windowFrom(p.Cursor, p.Limit),
	})
	if err != nil {
		return nil, err
	}
	coverage, err := s.ports.AuthAttempts.Coverage(ctx, site)
	if err != nil {
		return nil, err
	}
	blind, err := s.ports.AuthAttempts.BlindSpots(ctx, site)
	if err != nil {
		return nil, err
	}
	items := page.Items
	if items == nil {
		items = []AuthAttempt{}
	}
	if blind == nil {
		blind = []gen.AuthBlindSpot{}
	}
	return gen.ListAuthAttempts200JSONResponse{
		Items:       items,
		NextCursor:  page.NextCursor,
		MacCoverage: coverage,
		BlindSpots:  blind,
	}, nil
}
