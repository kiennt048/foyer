package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/netip"
	"time"

	"github.com/kiennt048/foyer/internal/aaa"
)

// Compatibility shims.
//
// `internal/aaa`, `internal/api` and `cmd/foyerctl` call a small surface of loose
// functions, kept here and spelled in terms of the repositories.
//
// **These are meant to be deleted** once the API is wired to the
// repositories directly. Each one is a thin translation and none of them adds
// behaviour, so deleting them is a matter of moving call sites rather than
// re-implementing anything. Where the old signature cannot express something the
// new schema knows -- a site, in particular -- the shim says so rather than
// pretending.

// LookupVoucher implements aaa.PolicyStore.
//
// Rx and Tx here are the guest's *download* and *upload*: the contract calls them
// downKbps and upKbps, and RADIUS attribute naming is from the NAS's point of view,
// which is the opposite. Getting this backwards gives every guest their upload
// speed as a download cap.
func (s *Store) LookupVoucher(ctx context.Context, code string) (*aaa.VoucherInfo, error) {
	var info aaa.VoucherInfo
	var duration, down, up *int
	err := s.pool.QueryRow(ctx, `
		SELECT v.code, v.state, v.expires_at, b.duration_minutes, b.down_kbps, b.up_kbps, b.site_id
		FROM vouchers v
		JOIN voucher_batches b ON b.id = v.batch_id
		WHERE v.code = $1`, code).
		Scan(&info.Code, &info.State, &info.ExpiresAt, &duration, &down, &up, &info.SiteID)
	if err != nil {
		return nil, mapErr(err)
	}
	if duration != nil {
		info.DurationMinutes = *duration
	}
	if down != nil {
		info.RxRateKbps = *down
	}
	if up != nil {
		info.TxRateKbps = *up
	}
	return &info, nil
}

// ClaimVoucher implements aaa.PolicyStore, and it is the portal's claim, not a copy
// of it.
//
// `GrantRepo.ClaimVoucher` is the one statement that tests `state = 'unused'` and
// writes `in-use`, `first_used_at` and a from-first-use expiry together. Calling it
// here is what makes a voucher single-use across the portal AND RADIUS rather than
// once per path: a code redeemed at the portal is `in-use`, and this finds nothing;
// a code used over RADIUS first is `in-use`, and the portal finds nothing. A
// RADIUS path that never claimed would break that.
//
// **With the site the request arrived at, and never without one.** A claim
// carrying `""` is read by `GrantRepo.ClaimVoucher` as "any venue" — so a code
// printed for site A would open site B's network. The engine resolves the site
// from the RADIUS client (RADIUSClientSite) and this refuses an empty one
// outright rather than passing the wildcard through: the portal may have reasons
// to claim unscoped, RADIUS never does.
//
// Zero rows is ErrNotFound, which IsIdentityNotFound reports as absence: the engine
// answers a refusal, not an outage.
func (s *Store) ClaimVoucher(ctx context.Context, siteID, code string, at time.Time) (*aaa.VoucherInfo, error) {
	if siteID == "" {
		return nil, ErrNotFound
	}
	c, err := s.grants.ClaimVoucher(ctx, siteID, code, "", at)
	if err != nil {
		return nil, err
	}
	info := &aaa.VoucherInfo{Code: code, State: "in-use", ExpiresAt: c.ExpiresAt, SiteID: siteID}
	if c.Plan.DurationMinutes != nil {
		info.DurationMinutes = *c.Plan.DurationMinutes
	}
	// Rx is the guest's download and Tx its upload; see LookupVoucher.
	if c.Plan.DownKbps != nil {
		info.RxRateKbps = *c.Plan.DownKbps
	}
	if c.Plan.UpKbps != nil {
		info.TxRateKbps = *c.Plan.UpKbps
	}
	return info, nil
}

// RADIUSClientSite implements aaa.PolicyStore: the site of the most specific
// enabled RADIUS client whose address covers clientIP.
//
// Most specific first, because that is how FreeRADIUS chose the client the packet
// was verified against: a /32 for one access point inside a /24 for the rest of a
// fleet is a different client, possibly at a different venue, and the /24 must not
// win. Disabled clients are not projected into `nas` (NasProjectionRepo), so
// FreeRADIUS cannot have matched one, and neither may this.
//
// An empty or unparsable address is ErrNotFound — a request with no source is a
// request with no venue, and the engine refuses a voucher for it.
func (s *Store) RADIUSClientSite(ctx context.Context, clientIP string) (string, error) {
	addr, err := netip.ParseAddr(clientIP)
	if err != nil {
		return "", ErrNotFound
	}
	var site string
	err = s.pool.QueryRow(ctx, `
		SELECT site_id FROM radius_clients
		 WHERE enabled AND $1::inet <<= address
		 ORDER BY masklen(address) DESC, id
		 LIMIT 1`, addr.Unmap().String()).Scan(&site)
	if err != nil {
		return "", mapErr(err)
	}
	return site, nil
}

// LookupGuestAccount implements aaa.PolicyStore.
//
// The old signature has no site, and accounts are unique per site rather than
// deployment-wide -- two venues may each have a `reception`. It resolves the oldest
// match, which is right for a single-site install and wrong for anything else. The
// site comes from the RADIUS client the request arrived from, and wiring that is
// still to be done.
//
// ── except that the account holding the RADIUS verifier comes first ──────────
//
// An allow for an account is not an accept: the
// generated virtual server has rlm_pap check User-Password against the verifier it
// reads from `radius_pap_verifiers` BY USERNAME, and that view's usernames are
// globally unique (migration 00016). So the account whose password is checked is
// always the one that owns the verifier row. Were the account AUTHORIZED here
// simply the oldest with that name, then with two venues each with a
// `reception`, the newer one opted in, the newer one's password would get a guest
// on under the older one's plan, rate limit and enabled flag, and the older one
// could never log in over RADIUS at all. The allow and the proof would be about
// two different identities.
//
// Ordering the verifier's owner first makes them the same identity: when a verifier
// exists for this username, this is its account (and if that account is disabled,
// the answer is a refusal, which is also what the view would give). When none
// exists there is nothing rlm_pap can verify and the account path refuses anyway.
// Binding the lookup to a site will not change this: a verifier owned by another site's
// account is then a refusal, never a fallback to this site's account.
func (s *Store) LookupGuestAccount(ctx context.Context, username string) (*aaa.GuestAccountInfo, error) {
	var info aaa.GuestAccountInfo
	var duration, down, up *int
	err := s.pool.QueryRow(ctx, `
		SELECT a.username, a.enabled, a.duration_minutes, a.down_kbps, a.up_kbps
		FROM accounts a
		WHERE a.username = $1
		ORDER BY EXISTS (SELECT 1 FROM account_pap_verifiers v WHERE v.account_id = a.id) DESC,
		         a.created_at
		LIMIT 1`, username).
		Scan(&info.Username, &info.Enabled, &duration, &down, &up)
	if err != nil {
		return nil, mapErr(err)
	}
	if duration != nil {
		info.DurationMinutes = *duration
	}
	if down != nil {
		info.RxRateKbps = *down
	}
	if up != nil {
		info.TxRateKbps = *up
	}
	return &info, nil
}

// IsMACAllowed implements aaa.PolicyStore.
//
// It answers true because there is no MAC allow-list in the schema and inventing
// one here would be a policy decision made in a shim. MAC-auth is not implemented,
// and `docs/rules/security.md` §6 is explicit that a MAC is a hint rather than an identity.
func (s *Store) IsMACAllowed(ctx context.Context, mac string) (bool, error) {
	return true, nil
}

// GetOperatorByUsername is the pre-repository spelling of Operators().GetByUsername.
func (s *Store) GetOperatorByUsername(ctx context.Context, username string) (*Operator, error) {
	return s.operators.GetByUsername(ctx, username)
}

// CreateOperator is the pre-repository spelling of Operators().Create.
func (s *Store) CreateOperator(ctx context.Context, op *Operator) error {
	return s.operators.Create(ctx, op)
}

// RecordOperatorLogin is the pre-repository spelling of Operators().RecordLogin.
func (s *Store) RecordOperatorLogin(ctx context.Context, id string) error {
	return s.operators.RecordLogin(ctx, id, time.Now())
}

// CreateSession is the pre-repository spelling of Authn().Create.
//
// The old signature has no CSRF token and the column is NOT NULL, so one is minted
// here. That is not a stopgap: the CSRF defence pairs the session cookie with a
// double-submit token on every state-changing operation, and a session row that
// could exist without one would be a session the CSRF check cannot be applied to.
func (s *Store) CreateSession(ctx context.Context, tokenHash, operatorID string, expiresAt time.Time) error {
	csrf, err := randomToken()
	if err != nil {
		return err
	}
	return s.authn.Create(ctx, &OperatorSession{
		TokenHash:  tokenHash,
		OperatorID: operatorID,
		CSRFToken:  csrf,
		ExpiresAt:  expiresAt,
	})
}

// GetSession is the pre-repository spelling of Authn().Resolve, returning only the
// operator.
func (s *Store) GetSession(ctx context.Context, tokenHash string) (*Operator, error) {
	_, op, err := s.authn.Resolve(ctx, tokenHash)
	return op, err
}

// DeleteSession is the pre-repository spelling of Authn().Delete.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	return s.authn.Delete(ctx, tokenHash)
}

// AppendAuditLog is the pre-repository spelling of Audit().Append.
func (s *Store) AppendAuditLog(ctx context.Context, entry *AuditEntry) error {
	if entry.SubjectKind == "" {
		entry.SubjectKind = "site"
	}
	return s.audit.Append(ctx, entry)
}

// ListAuthAttempts is the pre-repository spelling of Accounting().ListAuthAttempts.
func (s *Store) ListAuthAttempts(ctx context.Context, limit int) ([]AuthAttempt, error) {
	page, err := s.accounting.ListAuthAttempts(ctx, AuthAttemptFilter{ListOptions: ListOptions{Limit: limit}})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// IsIdentityNotFound implements aaa.IdentityNotFoundReporter.
//
// **This is the difference between an Access-Reject and an "I could not look".**
//
// The engine has three outcomes, not two: allow, authoritative denial, and
// undecided — and only the third reaches the generated config's failure branch,
// where a site's fail-open setting can apply. It cannot tell them apart on its own,
// because the dependency runs `store -> aaa`: this package imports that one, so
// `aaa` cannot call `errors.Is(err, store.ErrNotFound)` without a cycle. One method
// here is the whole adaptation.
//
// Getting it wrong is silent in both directions and only one of them is safe:
//
//   - **Reporting a database outage as "not found"** mints an authoritative
//     Access-Reject while the control plane knows nothing. That is what the previous
//     engine did — it tested `err == nil && voucher != nil` and fell through to
//     "identity not found" — so an outage produced confident denials and the failure
//     model was bypassed entirely.
//   - **Reporting a missing voucher as an outage** answers 503 for a typo. Harmless
//     while every site is fail-closed, because a wrong code still ends in
//     Access-Reject. It stops being harmless the moment a site is set fail-open:
//     a guest typing a code that does not exist would be let on to the network.
//
// The second is the one that arrived with migration 00007, which is why this landed
// in the same change.
//
// `errors.Is` rather than `==` because mapErr wraps ErrConflict with a constraint
// name and nothing stops a future caller wrapping this one too.
func (s *Store) IsIdentityNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
