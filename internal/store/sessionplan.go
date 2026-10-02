package store

import "context"

// The plan behind an accounting row.
//
// ── why this is a read model and not a column ────────────────────────────────
//
// `radacct` has no plan column and must never grow one: the stock FreeRADIUS
// column set is kept verbatim because it is the project's exit hatch, and Go does
// not write that table at all. So the plan a session was sold under has to be
// joined from Foyer's own tables at read time.
//
// **The only value in an accounting row that can key such a join is
// `radacct.username`.** That is not a new belief invented here — `identityKind`
// already reads exactly this field to decide whether a session is a voucher, an
// account or MAC-auth, and `internal/apistore`'s voucher filter already resolves a
// voucher through it: for a redeemed voucher the NAS puts the code itself in
// User-Name, and for a guest account it puts the account's username.
//
// ── what is deliberately not consulted, and why ──────────────────────────────
//
// `portal_grants` carries a per-guest **snapshot** of the plan, including an
// `enforced_by` frozen at the moment that guest was let on, which is a strictly
// better answer than the batch's or the account's. It is not used, because nothing
// links a grant to an accounting row: the grant records a MAC and a time, `radacct`
// records a MAC and a time, and joining those two is a guess dressed as a join —
// against a MAC that iOS and Android both randomise per SSID, in a fixture world
// that deliberately carries duplicate sessions for one address. A wrong plan on a
// session row is worse than no plan, because it is unfalsifiable from the screen.
//
// A key join is what is offered instead, and its scope is stated below.
type SessionPlanRepo struct{ s *Store }

// SessionPlans is constructed on demand rather than held on the Store.
//
// It owns no table. Every row it reads belongs to `VoucherRepo` or `AccountRepo`,
// and it exists only because the question — *what was the identity in this
// accounting row sold?* — spans both and belongs to neither. Adding it to the
// Store's field list would suggest a fourth owner where there are three.
func (s *Store) SessionPlans() *SessionPlanRepo { return &SessionPlanRepo{s: s} }

// IdentityRef is one `(site, username)` pair read off an accounting row.
//
// The site is half the key and not a convenience. `accounts` is
// `UNIQUE (tenant_id, site_id, username)` — unique per venue, **not** globally — so
// a lookup by username alone resolves one venue's guest against another venue's
// plan the first time two sites both have a "reception". The 802.1X verifier in
// `account_pap_verifiers` is the one identity in this schema that *is* globally
// unique, and migration `00016` had to say so explicitly for the same reason.
type IdentityRef struct {
	SiteID   string
	Username string
}

// PlansFor resolves each reference to the plan its identity was sold, by exact key.
//
// Absent from the returned map means **there is no plan**, which is the honest
// answer for MAC-auth, for 802.1X, and for any username Foyer does not own. Callers
// must render that as nothing rather than as an empty plan: an empty `Plan` reports
// `enforcedBy: unenforced`, which is a statement about a limit, and there is no
// limit here to make a statement about.
//
// Two lookups, in a fixed order:
//
//  1. `vouchers.code = username` in the same site, then that voucher's batch. The
//     plan is inline on the batch precisely so that a shared plan row cannot change
//     what a voucher sold last month, which is what makes this the record of the
//     sale rather than a current setting.
//  2. `accounts.username = username` in the same site.
//
// The order matters only for a username that is both a voucher code and an account
// name — a twelve-character upper-case account name would do it — and it is fixed
// rather than left to the planner so that the same row does not change plan between
// two reads. The voucher wins: a code is issued by Foyer and cannot be chosen, and
// an account name can.
//
// Matching is exact, including case, because that is what the uniqueness
// constraints behind both tables are: `UNIQUE (tenant_id, code)` and
// `UNIQUE (tenant_id, site_id, username)`. A case-insensitive match here would
// resolve pairs the database considers distinct.
func (r *SessionPlanRepo) PlansFor(ctx context.Context, refs []IdentityRef) (map[IdentityRef]Plan, error) {
	out := map[IdentityRef]Plan{}
	if len(refs) == 0 {
		return out, nil
	}
	sites := make([]string, 0, len(refs))
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.SiteID == "" || ref.Username == "" {
			continue
		}
		sites = append(sites, ref.SiteID)
		names = append(names, ref.Username)
	}
	if len(sites) == 0 {
		return out, nil
	}

	// One statement for the whole page. Per row it would be two round trips per
	// session, on the screen an operator refreshes while somebody waits.
	//
	// The two candidates are UNIONed and then `DISTINCT ON` takes the first by
	// rank, rather than each column being coalesced across them: a per-column
	// coalesce would fill a batch's null plan name from an account's, producing a
	// plan that no identity was ever sold.
	rows, err := r.s.pool.Query(ctx, `
		WITH want(site_id, username) AS (
			SELECT DISTINCT * FROM unnest($1::text[], $2::text[])
		),
		found AS (
			SELECT w.site_id, w.username, 1 AS rank,
			       b.plan_name, b.plan_name_lang, b.duration_minutes,
			       b.data_allowance_mb, b.down_kbps, b.up_kbps, b.enforced_by
			FROM want w
			JOIN vouchers v         ON v.site_id = w.site_id AND v.code = w.username
			JOIN voucher_batches b  ON b.id = v.batch_id
			UNION ALL
			SELECT w.site_id, w.username, 2,
			       a.plan_name, a.plan_name_lang, a.duration_minutes,
			       a.data_allowance_mb, a.down_kbps, a.up_kbps, a.enforced_by
			FROM want w
			JOIN accounts a         ON a.site_id = w.site_id AND a.username = w.username
		)
		SELECT DISTINCT ON (site_id, username)
		       site_id, username, plan_name, plan_name_lang, duration_minutes,
		       data_allowance_mb, down_kbps, up_kbps, enforced_by
		FROM found
		ORDER BY site_id, username, rank`, sites, names)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	for rows.Next() {
		var ref IdentityRef
		var p Plan
		if err := rows.Scan(&ref.SiteID, &ref.Username, &p.Name, &p.NameLang,
			&p.DurationMinutes, &p.DataAllowanceMB, &p.DownKbps, &p.UpKbps, &p.EnforcedBy); err != nil {
			return nil, mapErr(err)
		}
		out[ref] = p
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
