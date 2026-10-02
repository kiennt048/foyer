package store

import (
	"context"
	"time"
)

// GrantRepo owns `portal_sessions`, `portal_grants` and `authorization_events`,
// and the one statement that claims a voucher.
//
// The third table is here rather than in a repository of its own because its write
// and `portal_grants.delivery` are one transaction and must stay one -- see
// authorizations.go.
//
// ── why these shapes are not in models.go ────────────────────────────────────
//
// models.go says its field sets follow `web/contract/openapi.yaml`, because a
// store type carrying a field the contract does not define is a field some screen
// will eventually render. **The grant is not in the contract at all** —
// `PortalStatus` is a projection of one and deliberately omits which AP, which
// NAS, which voucher and when it was revoked — so these types belong beside the
// repository that reads them rather than beside the contract's nouns.
//
// ── what this repository refuses to offer ────────────────────────────────────
//
// There is no method that takes a MAC and returns a session. A MAC arrives from
// the NAS as an unsigned query parameter, and resolving a browser's state from a
// value somebody typed is the whole of `docs/rules/security.md` §4. The token
// hash is the only key, and the schema has no index that would make the other
// lookup cheap.
type GrantRepo struct{ s *Store }

// PortalSession is one browser, bound to one MAC.
type PortalSession struct {
	// TokenHash is sha256 of the cookie value, hex. The cookie is a bearer
	// credential for a MAC binding, so a database dump must not contain a live
	// one.
	TokenHash string

	SiteID string
	NASID  *string
	// VendorSiteID is the controller's own site identifier — UniFi's, not
	// Foyer's. Carried separately because the driver takes one and the database
	// keys on the other.
	VendorSiteID *string
	// ControllerID names the controller that managed this access point when the
	// session was minted. It is stored rather than resolved per call so that the
	// controller which confirmed the device present is the controller asked to let
	// it on -- see migration 00012.
	ControllerID *string
	// AccountingConfigured is the RADIUS client's own measurement, read through
	// a join rather than stored here. Three-valued: nil is *not measured*, which
	// is not *not configured*. See portalSessionColumns.
	AccountingConfigured *bool
	ClientMAC            string
	APMAC                *string
	SSID                 *string
	OriginalURL          *string
	GrantID              *string

	CreatedAt time.Time
	ExpiresAt time.Time
}

// PortalGrant is one decision to let one device onto the network.
type PortalGrant struct {
	ID     string
	SiteID string
	NASID  *string

	ClientMAC string
	APMAC     *string

	Method    string // voucher | account | click-through | phone
	VoucherID *string
	AccountID *string
	// Phone is the E.164 number a phone guest verified. Set exactly when Method
	// is `phone`; migration 00022 holds the two together.
	Phone *string

	Plan Plan

	GrantedAt time.Time
	ExpiresAt *time.Time
	RevokedAt *time.Time

	// Delivery is whether the network was actually opened: `open`, `failed`, or
	// nil. **Nil is the zero value on purpose** and covers two situations the
	// portal cannot tell apart -- the driver call has not returned yet, which is
	// what a *successful* redemption looks like for the length of one controller
	// round trip, and a deployment whose `Authorizations` port is not wired. Both
	// read `connected`, because *nothing has told us it failed* is not *it failed*.
	//
	// It is never written by CreateGrant. It arrives through
	// RecordAuthorization, in the same transaction as the event row an operator
	// reads -- see authorizations.go.
	Delivery *string
}

// VoucherClaim is what one successful claim returns: the voucher, and the plan it
// was sold under, from the same statement.
type VoucherClaim struct {
	VoucherID string
	BatchID   string
	Plan      Plan
	// ExpiresAt is the voucher's own expiry after the claim, which a
	// `from-first-use` batch sets *by* claiming.
	ExpiresAt *time.Time
}

// VoucherState is why a claim found nothing, for the second read that only decides
// which sentence a guest reads.
type VoucherState struct {
	Known bool
	// State is the voucher's stored state: unused, in-use, exhausted, expired,
	// revoked. Meaningless unless Known.
	State string
	// Expired reports that the row's own timestamp has passed, which is true
	// before any sweep has moved the state column.
	Expired bool
}

// `rc.accounting_configured` is read through the join rather than stored on the
// row, and the difference is not a saving. `Plan.EnforcedBy` is decided when the
// grant is made, and the NAS probe that writes that column may have run since the
// session was minted; a copy taken at the front door would report an enforcement
// answer half an hour out of date. The three ids above are frozen on purpose --
// they decide which controller is asked, and that has to be the one that confirmed
// the device present -- and this one is deliberately not.
const portalSessionColumns = `
	s.token_hash, s.site_id, s.nas_id, s.vendor_site_id, s.controller_id,
	rc.accounting_configured,
	s.client_mac::text, s.ap_mac::text,
	s.ssid, s.original_url, s.grant_id, s.created_at, s.expires_at`

const portalSessionFrom = `
	FROM portal_sessions s
	LEFT JOIN radius_clients rc ON rc.id = s.nas_id`

func scanPortalSession(row interface{ Scan(...any) error }) (*PortalSession, error) {
	var s PortalSession
	err := row.Scan(&s.TokenHash, &s.SiteID, &s.NASID, &s.VendorSiteID, &s.ControllerID,
		&s.AccountingConfigured,
		&s.ClientMAC, &s.APMAC, &s.SSID, &s.OriginalURL, &s.GrantID, &s.CreatedAt, &s.ExpiresAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &s, nil
}

// CreateSession mints the browser-to-MAC binding.
func (r *GrantRepo) CreateSession(ctx context.Context, s *PortalSession) error {
	return mapErr(r.s.pool.QueryRow(ctx, `
		INSERT INTO portal_sessions
			(token_hash, site_id, nas_id, vendor_site_id, controller_id, client_mac, ap_mac,
			 ssid, original_url, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6::macaddr, $7::macaddr, $8, $9, $10)
		RETURNING created_at`,
		s.TokenHash, s.SiteID, s.NASID, s.VendorSiteID, s.ControllerID, NormaliseMAC(s.ClientMAC),
		macOrNil(s.APMAC), s.SSID, s.OriginalURL, s.ExpiresAt).Scan(&s.CreatedAt))
}

// Session resolves a token hash, and refuses a row past its bound.
//
// The expiry is in the WHERE clause rather than checked after the read, so an
// expired row and a token that never existed are the same `ErrNotFound` by
// construction. A caller that could tell them apart could tell whether a token was
// ever valid, on an endpoint reachable by anyone within radio range.
func (r *GrantRepo) Session(ctx context.Context, tokenHash string, at time.Time) (*PortalSession, error) {
	return scanPortalSession(r.s.pool.QueryRow(ctx,
		`SELECT `+portalSessionColumns+portalSessionFrom+`
		 WHERE s.token_hash = $1 AND s.expires_at > $2`, tokenHash, at))
}

// AttachGrant binds a grant to the browser that earned it.
func (r *GrantRepo) AttachGrant(ctx context.Context, tokenHash, grantID string) error {
	tag, err := r.s.pool.Exec(ctx,
		`UPDATE portal_sessions SET grant_id = $2 WHERE token_hash = $1`, tokenHash, grantID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ForgetSession drops a binding.
//
// Called when a request carries a session for one MAC and NAS parameters naming
// another: the row is forgotten rather than re-pointed, and the browser has to
// mint a new session that passes VerifyPresence on its own.
func (r *GrantRepo) ForgetSession(ctx context.Context, tokenHash string) error {
	_, err := r.s.pool.Exec(ctx, `DELETE FROM portal_sessions WHERE token_hash = $1`, tokenHash)
	return mapErr(err)
}

// ReapSessions deletes bindings past their bound.
//
// The read path already refuses them, so this is about the table's size rather
// than about correctness — the pre-authentication state is reachable by anyone
// within radio range and nothing else bounds how many rows it can hold.
func (r *GrantRepo) ReapSessions(ctx context.Context, at time.Time) (int64, error) {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM portal_sessions WHERE expires_at <= $1`, at)
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}

const portalGrantColumns = `
	id, site_id, nas_id, client_mac::text, ap_mac::text, method, voucher_id, account_id,
	plan_name, plan_name_lang, duration_minutes, data_allowance_mb, up_kbps, down_kbps,
	enforced_by, granted_at, expires_at, revoked_at, delivery, phone`

func scanPortalGrant(row interface{ Scan(...any) error }) (*PortalGrant, error) {
	var g PortalGrant
	err := row.Scan(&g.ID, &g.SiteID, &g.NASID, &g.ClientMAC, &g.APMAC, &g.Method,
		&g.VoucherID, &g.AccountID, &g.Plan.Name, &g.Plan.NameLang,
		&g.Plan.DurationMinutes, &g.Plan.DataAllowanceMB, &g.Plan.UpKbps, &g.Plan.DownKbps,
		&g.Plan.EnforcedBy, &g.GrantedAt, &g.ExpiresAt, &g.RevokedAt, &g.Delivery, &g.Phone)
	if err != nil {
		return nil, mapErr(err)
	}
	return &g, nil
}

// CreateGrant records what the venue decided.
//
// The unique index on `portal_grants (voucher_id) WHERE voucher_id IS NOT NULL` is
// what makes a second grant for one voucher a `23505` rather than a second guest
// online, so a conflict here is ErrConflict and the caller refuses.
func (r *GrantRepo) CreateGrant(ctx context.Context, g *PortalGrant) error {
	enforced := g.Plan.EnforcedBy
	if enforced == "" {
		enforced = "unenforced"
	}
	return mapErr(r.s.pool.QueryRow(ctx, `
		INSERT INTO portal_grants
			(id, site_id, nas_id, client_mac, ap_mac, method, voucher_id, account_id,
			 plan_name, plan_name_lang, duration_minutes, data_allowance_mb,
			 up_kbps, down_kbps, enforced_by, granted_at, expires_at, phone)
		VALUES ($1, $2, $3, $4::macaddr, $5::macaddr, $6, $7, $8, $9, $10, $11, $12,
		        $13, $14, $15, $16, $17, $18)
		RETURNING granted_at`,
		g.ID, g.SiteID, g.NASID, NormaliseMAC(g.ClientMAC), macOrNil(g.APMAC),
		g.Method, g.VoucherID, g.AccountID,
		g.Plan.Name, g.Plan.NameLang, g.Plan.DurationMinutes, g.Plan.DataAllowanceMB,
		g.Plan.UpKbps, g.Plan.DownKbps, enforced, g.GrantedAt, g.ExpiresAt, g.Phone).
		Scan(&g.GrantedAt))
}

// Grant returns one grant by id.
func (r *GrantRepo) Grant(ctx context.Context, id string) (*PortalGrant, error) {
	return scanPortalGrant(r.s.pool.QueryRow(ctx,
		`SELECT `+portalGrantColumns+` FROM portal_grants WHERE id = $1`, id))
}

// RevokeGrant ends a grant. Revocation is stored because it is an event; expiry is
// derived because it is only the clock passing a number.
func (r *GrantRepo) RevokeGrant(ctx context.Context, id string, at time.Time) error {
	tag, err := r.s.pool.Exec(ctx,
		`UPDATE portal_grants SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, id, at)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ClaimVoucher is one statement, and the naming is the guarantee.
//
// `docs/rules/security.md` §9 requires single use "enforced transactionally,
// not with a read-then-write". This tests the state and changes it in the same
// UPDATE, joined to the batch so the claim and the plan come out together —
// reading the batch separately would let a batch edited between the two grant a
// plan the voucher was not sold under.
//
// Zero rows means somebody else won, or the code was never claimable. **Which of
// those is a separate, read-only question** (see VoucherState) that only decides
// which sentence the guest reads.
//
// `WHERE v.code = $2` against an indexed column is not constant time and cannot be
// made so — the index is what makes the portal answer in single-digit
// milliseconds. On this path the defences are the portal's rate limiter and the
// code's own entropy: twelve characters from a 31-symbol alphabet, from
// crypto/rand. Written down here rather than assumed.
// The claiming MAC is a parameter and is deliberately not written to `vouchers`.
// There is no column for it — `vouchers.claimed_mac` does not exist (CONTRACT.md) —
// and `current_session_id` is not it: that names a `radacct` row, which does not
// exist at the moment a voucher is claimed.
// The MAC is on `portal_grants`, which is the row that records this decision, and a
// second copy would be a second source of truth. Kept in the signature because the
// port declares it and because a column may yet be the right answer.
func (r *GrantRepo) ClaimVoucher(ctx context.Context, siteID, code, _ string, at time.Time) (*VoucherClaim, error) {
	var c VoucherClaim
	err := r.s.pool.QueryRow(ctx, `
		UPDATE vouchers v
		   SET state         = 'in-use',
		       first_used_at = coalesce(v.first_used_at, $3),
		       expires_at    = CASE WHEN b.expiry_mode = 'from-first-use'
		                            THEN $3 + make_interval(mins => b.duration_minutes)
		                            ELSE v.expires_at END
		  FROM voucher_batches b
		 WHERE v.batch_id = b.id
		   AND ($1 = '' OR b.site_id = $1)
		   AND v.code  = $2
		   AND v.state = 'unused'
		   AND (v.expires_at IS NULL OR v.expires_at > $3)
		RETURNING v.id, b.id, b.plan_name, b.plan_name_lang, b.duration_minutes,
		          b.data_allowance_mb, b.up_kbps, b.down_kbps, v.expires_at`,
		siteID, code, at).
		Scan(&c.VoucherID, &c.BatchID, &c.Plan.Name, &c.Plan.NameLang,
			&c.Plan.DurationMinutes, &c.Plan.DataAllowanceMB, &c.Plan.UpKbps,
			&c.Plan.DownKbps, &c.ExpiresAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &c, nil
}

// VoucherStateFor is the second, read-only query: why a claim found nothing.
//
// It is outside the claim on purpose. It changes nothing and decides nothing about
// access — it only chooses between "check each character" and "ask staff for
// another one", which are different instructions and the wrong one wastes
// somebody's time at a counter.
//
// A voucher belonging to another site answers `Known: false`, the same as one that
// never existed: an answer that distinguished them would tell a stranger which
// codes exist somewhere else.
func (r *GrantRepo) VoucherStateFor(ctx context.Context, siteID, code string, at time.Time) (VoucherState, error) {
	var out VoucherState
	err := r.s.pool.QueryRow(ctx, `
		SELECT state, (expires_at IS NOT NULL AND expires_at <= $3)
		FROM vouchers
		WHERE code = $2 AND ($1 = '' OR site_id = $1)`, siteID, code, at).
		Scan(&out.State, &out.Expired)
	switch {
	case err == nil:
		out.Known = true
		return out, nil
	case mapErr(err) == ErrNotFound:
		return VoucherState{}, nil
	default:
		return VoucherState{}, mapErr(err)
	}
}

// GuestPlacement is where a guest is standing, resolved from the access point MAC
// the NAS named.
//
// It carries the controller as well as the site because the driver seam needs one,
// and it carries the RADIUS client's accounting measurement because
// `Plan.EnforcedBy` needs that: a limited plan behind a driver that cannot enforce
// it is only `foyer-sampled` if accounting actually arrives. Resolving all of it in
// one statement is what stops the portal reading `access_points` once per question.
type GuestPlacement struct {
	SiteID       string
	NASID        *string
	ControllerID *string
	VendorSiteID *string
	// AccountingConfigured is three-valued and nil means **not measured**, which is
	// not the same as not configured. It is `radius_clients.accounting_configured`
	// as the NAS probe last wrote it, and it is nil for an access point whose
	// clients.conf row is unknown as well -- 00004 leaves that binding unset for a
	// proxying controller rather than guessing, and an absent binding is not
	// evidence that no accounting arrives.
	AccountingConfigured *bool
	// APState is the access point's own state: online, offline, missing, unknown.
	// A missing AP still resolves — a device that stopped appearing in discovery
	// is not a device that was deleted, and its MAC is what resolves last week's
	// sessions.
	APState string
}

// PlacementByAPMAC resolves the identifier the NAS redirect actually carries.
//
// An unknown access point is ErrNotFound and the caller refuses. There is no
// fallback to "the only site": a portal that defaulted for an AP it has never
// heard of would authorize devices on a network it does not manage, and in a
// single-site deployment that default is invisible until the second site exists.
func (r *GrantRepo) PlacementByAPMAC(ctx context.Context, apMAC string) (*GuestPlacement, error) {
	var p GuestPlacement
	err := r.s.pool.QueryRow(ctx, `
		SELECT ap.site_id, ap.radius_client_id, ap.controller_id, cvs.vendor_site_id,
		       rc.accounting_configured, ap.state
		FROM access_points ap
		LEFT JOIN controller_vendor_sites cvs
		       ON cvs.controller_id = ap.controller_id
		      AND cvs.bound_site_id = ap.site_id
		LEFT JOIN radius_clients rc ON rc.id = ap.radius_client_id
		WHERE ap.mac = $1::macaddr`, NormaliseMAC(apMAC)).
		Scan(&p.SiteID, &p.NASID, &p.ControllerID, &p.VendorSiteID,
			&p.AccountingConfigured, &p.APState)
	if err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}

// AccountIDForUsername resolves a guest login to the row a grant references.
//
// It exists because `portal.Grant` carries a username and `portal_grants` carries
// an account id, and the CHECK constraint requires the id. The narrow fix is a
// field on the portal's own type, and this is what stands in until then.
func (r *GrantRepo) AccountIDForUsername(ctx context.Context, siteID, username string) (string, error) {
	var id string
	err := r.s.pool.QueryRow(ctx,
		`SELECT id FROM accounts WHERE site_id = $1 AND username = $2`, siteID, username).Scan(&id)
	return id, mapErr(err)
}

// macOrNil keeps an absent MAC out of the macaddr cast. An empty string is not an
// address, and PostgreSQL would refuse it with its own message rather than storing
// the absence the caller meant.
func macOrNil(mac *string) *string {
	if mac == nil || *mac == "" {
		return nil
	}
	return ptr(NormaliseMAC(*mac))
}
