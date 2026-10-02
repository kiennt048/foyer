package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// `authorization_events`, and the grant column it may never be written without.
//
// ── why this is on GrantRepo and not a repository of its own ─────────────────
//
// The operator's event and the grant's `delivery` are two readers of **one fact**,
// and `internal/portal/CONTRACT.md` invariant 10 is explicit that an implementation
// able to write one without the other is one that can make them disagree: a console
// showing `authorize-failed` beside a portal showing a green tick, which the port's
// author judged worse than the silence it replaces. The port therefore has one
// method, and the SQL behind it is one transaction over two tables.
//
// A second repository could only have that property by reaching into this one's
// table, so the write lives with the table it must stay consistent with. That is
// the whole reason, and it is worth more than the tidiness of a file per aggregate.
//
// ── what this table is not ───────────────────────────────────────────────────
//
// It is not an extension of `radpostauth`. That column set is stock FreeRADIUS's
// and is kept verbatim as the project's exit hatch; `foyer_app` holds SELECT on it
// and nothing else, and nothing here writes it. The two streams are unioned at read
// time, which is what `AuthAttempt.source` has said all along -- `radius` for what
// FreeRADIUS saw, `driver` for what the controller answered.
//
// ── and why the read is not here ─────────────────────────────────────────────
//
// `ListAuthAttempts` in accounting.go is the union, and it has to be: one operation
// over two tables can have exactly one cursor, and a caller that fetched a page from
// each and merged them in Go could not name a resume point inside either. So each
// half of this table lives with the table it cannot be separated from -- the write
// with `portal_grants`, because they are one transaction, and the read with
// `radpostauth`, because they are one page.

// AuthorizationEvent is one attempt to open the network for one grant.
//
// **No field can hold the controller's own text.** `nas.APIError` embeds the URL it
// was reached at and up to 512 bytes of response body; a URL can carry an API key in
// its query string and a body is text somebody else wrote. `Failure` is a
// classification `internal/portal` assigns and `Status` is an integer the controller
// answered with, and there is nothing else -- the same shape `AuthAttempt` uses to
// make a submitted password unrepresentable rather than merely unwritten.
type AuthorizationEvent struct {
	// ID is assigned by the sequence. Zero on the way in.
	ID int64

	SiteID  string
	GrantID string
	NASID   *string

	ClientMAC string
	APMAC     *string

	Method    string // voucher | account | click-through
	VoucherID *string
	// Username is the identity offered, never the credential.
	Username *string

	At time.Time

	// Delivery is `open` or `failed`. Never empty: this row exists because a call
	// returned.
	Delivery string
	// Attempts includes the one that succeeded or the last one that failed.
	Attempts int

	// Failure is nil on `open`, and `unreachable` or `refused` on `failed`. The
	// database holds the two together: a `failed` row with no failure kind is one
	// an operator can do nothing with.
	Failure *string
	// Status is what a controller that *answered* answered with. Nil where nothing
	// answered, which the database also enforces -- a status beside `unreachable`
	// would be a number invented from a transport error.
	Status *int
}

// RecordAuthorization writes both readers, in one transaction.
//
// The order is deliberate. The `UPDATE` goes first so that a grant that does not
// exist is `ErrNotFound` naming the grant, rather than a foreign-key violation
// naming a constraint -- and so that no event row can survive a rollback caused by
// the grant being absent.
//
// A grant whose delivery is already written is overwritten rather than refused.
// `Service.deliver` calls this once per redemption, but a future re-authorization
// path would be a second real answer for the same grant, and the newest answer is
// the true one; the event rows keep the history either way.
func (r *GrantRepo) RecordAuthorization(ctx context.Context, e *AuthorizationEvent) error {
	return mapErr(r.s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE portal_grants SET delivery = $2 WHERE id = $1`, e.GrantID, e.Delivery)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Nothing is written at all. An event whose grant is gone would be an
			// operator-visible failure with no guest-visible half, which is the
			// disagreement this method exists to make impossible.
			return ErrNotFound
		}
		return tx.QueryRow(ctx, `
			INSERT INTO authorization_events
				(site_id, grant_id, nas_id, client_mac, ap_mac, method, voucher_id,
				 username, at, delivery, attempts, failure, status)
			VALUES ($1, $2, $3, $4::macaddr, $5::macaddr, $6, $7, $8, $9, $10, $11, $12, $13)
			RETURNING id`,
			e.SiteID, e.GrantID, e.NASID, NormaliseMAC(e.ClientMAC), macOrNil(e.APMAC),
			e.Method, e.VoucherID, e.Username, e.At, e.Delivery, e.Attempts,
			e.Failure, e.Status).Scan(&e.ID)
	}))
}
