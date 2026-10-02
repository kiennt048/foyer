package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ── the second audited exception ─────────────────────────────────────────────
//
// **This is the only Go file in the repository allowed to delete from `radacct` or
// `radpostauth`**, and it may only delete. Everything that removes a FreeRADIUS row
// -- a retention period running out, a data subject asking to be erased -- arrives
// here, and every statement that does it is a constant in this file, readable in one
// sitting.
//
// This is a deliberate design decision: the rule "Go never writes radacct or
// radpostauth" had one exception, the stale-session reaper, and a retention period
// cannot be honoured without a second. It was extended deliberately and in writing
// rather than quietly, and it is bounded the same three ways the reaper is:
//
//   - **One file.** The repository's write-time guards refuse a DELETE on either
//     table in every other Go file, and refuse INSERT, UPDATE and TRUNCATE on both tables in this one too.
//   - **PostgreSQL enforces it independently.** A `Retention` runs as
//     `foyer_retention` (migration 00020), which holds SELECT and DELETE on the two
//     tables and nothing else. `OpenRetention` checks that at connect time and
//     refuses a connection that could also insert, rewrite or truncate them --
//     including a superuser's -- so the privilege this file runs with is the
//     privilege it claims.
//   - **Everything it removes is counted, and the caller logs and records it.** A
//     sweep whose log says nothing and a bug that emptied the table look the same
//     from outside.
//
// ── why deleting and not pseudonymising in place ─────────────────────────────
//
// PacketFence's `acct_cleanup` and `auth_log_cleanup` delete rows older than a
// window, in batches, on a timer (conf/pfcron.conf.defaults: windows 1D and 1W,
// batch 100). Rewriting `username` or `callingstationid` to a placeholder would keep
// the byte counts, and it would need column privileges on what FreeRADIUS writes --
// the exact privilege the reaper was built to avoid holding. A deleted row is also
// the only state stock FreeRADIUS tooling, reading these tables after Foyer is gone,
// cannot misread as a real session.
//
// It also deletes from the two tables Foyer itself made append-only for the
// application: `audit_log` (00003) and `authorization_events` (00014). The
// application can still only SELECT and INSERT there.

// RetentionRole is the PostgreSQL role this file runs as.
const RetentionRole = "foyer_retention"

// ErrRetentionPrivilege is returned by OpenRetention when the connection holds a
// privilege this file must not run with, or lacks one it needs.
var ErrRetentionPrivilege = errors.New("store: the retention connection's privileges are not foyer_retention's")

// Retention is the deleting half of the retention and erasure feature. It is a
// separate value from Store on purpose: the application's store cannot reach these
// methods, and this one reaches nothing else.
type Retention struct {
	s *Store
}

// OpenRetention connects as foyer_retention.
//
// cfg.Role must be empty (meaning foyer_retention), foyer_retention itself, or
// NoRole for a deployment that issues the role a login of its own. Whatever the
// connection ends up being, it is asked -- not assumed -- whether it can DELETE from
// both FreeRADIUS tables and whether it can do anything else to either, and it is
// refused unless the answers are yes and no. A superuser answers yes to everything,
// so a harness that opened this as the schema owner is refused as well.
func OpenRetention(ctx context.Context, cfg Config) (*Retention, error) {
	switch cfg.Role {
	case "":
		cfg.Role = RetentionRole
	case RetentionRole, NoRole:
	default:
		return nil, fmt.Errorf("%w: asked to run as %q", ErrRetentionPrivilege, cfg.Role)
	}
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 2
	}
	s, err := Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := assertRetentionPrivileges(ctx, s); err != nil {
		s.Close()
		return nil, err
	}
	return &Retention{s: s}, nil
}

// retentionPrivilegeQuery answers OpenRetention's questions on the connection
// itself, after SET ROLE, so current_user is the role in force.
const retentionPrivilegeQuery = `
	SELECT current_user::text,
	       has_table_privilege(current_user, 'radacct', 'DELETE')
	         AND has_table_privilege(current_user, 'radpostauth', 'DELETE'),
	       has_table_privilege(current_user, 'radacct', 'INSERT')
	         OR has_table_privilege(current_user, 'radacct', 'TRUNCATE')
	         OR has_any_column_privilege(current_user, 'radacct', 'UPDATE')
	         OR has_table_privilege(current_user, 'radpostauth', 'INSERT')
	         OR has_table_privilege(current_user, 'radpostauth', 'TRUNCATE')
	         OR has_any_column_privilege(current_user, 'radpostauth', 'UPDATE')`

func assertRetentionPrivileges(ctx context.Context, s *Store) error {
	var who string
	var canDelete, canWrite bool
	if err := s.pool.QueryRow(ctx, retentionPrivilegeQuery).Scan(&who, &canDelete, &canWrite); err != nil {
		return fmt.Errorf("store: checking the retention connection's privileges: %w", err)
	}
	if !canDelete {
		return fmt.Errorf("%w: %s cannot DELETE from radacct and radpostauth (is migration 00020 applied?)",
			ErrRetentionPrivilege, who)
	}
	if canWrite {
		return fmt.Errorf("%w: %s can also insert into, rewrite or truncate a FreeRADIUS table, "+
			"and this file must run with DELETE only", ErrRetentionPrivilege, who)
	}
	return nil
}

// Close releases the connection pool.
func (r *Retention) Close() {
	if r != nil {
		r.s.Close()
	}
}

// ── retention: rows older than a cutoff ──────────────────────────────────────
//
// Every sweep deletes in batches through a keyed subquery, the shape PacketFence's
// maintenance tasks use, so one pass never holds a lock across the whole table while
// FreeRADIUS is inserting into it.
//
// **No `FOR UPDATE SKIP LOCKED`, unlike reaper.sql, and the reason is the grant.**
// PostgreSQL requires UPDATE privilege for a locking SELECT, and this role has none
// by design. Two control planes sweeping at once during a blue/green cutover still
// cannot double-count: the second DELETE waits for the first's row locks and then
// finds the rows gone.

// Closed sessions only. An open session is either live or waiting for the reaper;
// deleting it would have the console say a guest is not connected while FreeRADIUS
// keeps updating a row that no longer exists. `acctstarttime < $1` follows from a stop
// time before the cutoff and lets radacct_starttime_idx answer the scan.
const sweepAccountingStmt = `
	DELETE FROM radacct WHERE radacctid IN (
		SELECT radacctid FROM radacct
		WHERE acctstoptime IS NOT NULL AND acctstoptime < $1 AND acctstarttime < $1
		ORDER BY acctstarttime
		LIMIT $2)`

const sweepAuthLogStmt = `
	DELETE FROM radpostauth WHERE id IN (
		SELECT id FROM radpostauth WHERE authdate < $1 ORDER BY authdate LIMIT $2)`

const sweepAuthorizationEventsStmt = `
	DELETE FROM authorization_events WHERE id IN (
		SELECT id FROM authorization_events WHERE at < $1 ORDER BY at LIMIT $2)`

const sweepAuditStmt = `
	DELETE FROM audit_log WHERE id IN (
		SELECT id FROM audit_log WHERE at < $1 ORDER BY at LIMIT $2)`

// SweepAccounting deletes closed accounting sessions that ended before cutoff.
func (r *Retention) SweepAccounting(ctx context.Context, cutoff time.Time) (int64, error) {
	return sweepInBatches(ctx, r.s.pool, sweepAccountingStmt, cutoff)
}

// SweepAuthLog deletes FreeRADIUS post-auth rows older than cutoff.
func (r *Retention) SweepAuthLog(ctx context.Context, cutoff time.Time) (int64, error) {
	return sweepInBatches(ctx, r.s.pool, sweepAuthLogStmt, cutoff)
}

// SweepAuthorizationEvents deletes controller authorization events older than
// cutoff. They are the second stream `listAuthAttempts` reads, so they share the
// post-auth log's retention category.
func (r *Retention) SweepAuthorizationEvents(ctx context.Context, cutoff time.Time) (int64, error) {
	return sweepInBatches(ctx, r.s.pool, sweepAuthorizationEventsStmt, cutoff)
}

// SweepAudit deletes audit entries older than cutoff. Nothing removes one entry: the
// log stays uneditable, and only its age ends it.
func (r *Retention) SweepAudit(ctx context.Context, cutoff time.Time) (int64, error) {
	return sweepInBatches(ctx, r.s.pool, sweepAuditStmt, cutoff)
}

// ── erasure: every row that names one data subject ───────────────────────────
//
// **Matching is by normalised value, not by the spelling list `ActiveSessionForMAC`
// uses.** That function trades completeness for an index because a miss there fails
// closed (no session found, no grant). A miss here fails *open*: a spelling nobody
// listed is a row that survives an erasure the operator was told succeeded. So the
// column is normalised -- every non-hex character stripped, lower-cased -- and
// compared with the subject's twelve hex digits. It is a sequential scan; an erasure
// is rare, and the operator is waiting for a complete answer rather than a fast one.
//
// `username` is matched as a MAC too, but only when it is *shaped* like one: MAC
// authentication (MAB, and PacketFence's MAC-auth path) sends the device's MAC as
// User-Name, so accounting for a MAC-authenticated guest names them there and nowhere
// else. The shape test stops a username that merely consists of hex digits and
// punctuation from matching.
const macShapeSQL = `'^([0-9a-f]{2}[-:]?){5}[0-9a-f]{2}$|^[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}$'`

func macNorm(column string) string {
	return `lower(regexp_replace(coalesce(` + column + `, ''), '[^0-9A-Fa-f]', '', 'g'))`
}

var (
	eraseAccountingByMACStmt = `DELETE FROM radacct
		WHERE ` + macNorm("callingstationid") + ` = $1
		   OR (lower(coalesce(username, '')) ~ ` + macShapeSQL + ` AND ` + macNorm("username") + ` = $1)`
	eraseAuthLogByMACStmt = `DELETE FROM radpostauth
		WHERE ` + macNorm("callingstationid") + ` = $1
		   OR (lower(username) ~ ` + macShapeSQL + ` AND ` + macNorm("username") + ` = $1)`
)

const eraseAuthorizationEventsByMACStmt = `DELETE FROM authorization_events WHERE client_mac = $1::macaddr`

// By account: the username the account signs in with. Accounting and post-auth carry
// it verbatim as User-Name; there is no other column that ties a FreeRADIUS row to a
// Foyer account.
//
// **Scoped to the account's venue.** A guest account's username is unique per venue,
// not per deployment (00005: `UNIQUE (tenant_id, site_id, username)`), so `room101` at
// one hotel and `room101` at the next are two people, and matching on the username
// alone erased the second one's history along with the first's. A row belongs to
// another venue when the console would say so -- accounting by the RADIUS client its
// NAS address resolves to (longest prefix, as `clientForNAS`), post-auth by the access
// point in its Called-Station-Id (`calledStationParts`, the one place that spelling is
// known), authorization events by their own `site_id` -- and such a row is left alone.
// A row that resolves to **no** venue is erased: nobody can say it is somebody else's,
// and a miss here fails open. `$2` is the account's site.
//
// The two lookups are why foyer_retention holds column-level SELECT on
// `radius_clients (tenant_id, site_id, address)` and `access_points (tenant_id,
// site_id, mac)` (00020): read-only, and no column a secret or a credential lives in.
var (
	eraseAccountingByUsernameStmt = `DELETE FROM radacct WHERE radacctid IN (
		SELECT ra.radacctid FROM radacct ra
		LEFT JOIN LATERAL (
			SELECT c.site_id FROM radius_clients c
			WHERE c.address >>= ra.nasipaddress
			ORDER BY masklen(c.address) DESC
			LIMIT 1
		) rc ON true
		WHERE ra.username = $1 AND coalesce(rc.site_id, $2) = $2)`
	eraseAuthLogByUsernameStmt = `DELETE FROM radpostauth WHERE id IN (
		SELECT rp.id FROM radpostauth rp` + calledStationParts("rp") + `
		WHERE rp.username = $1 AND coalesce(ap.site_id, $2) = $2)`
)

const eraseAuthorizationEventsByUsernameStmt = `DELETE FROM authorization_events WHERE username = $1 AND site_id = $2`

// By phone number: every row whose username is one of the number's spellings, at
// every venue — a number is one person's wherever they used it. Exact spellings, so
// `radacct`'s username index answers rather than a scan of the whole history.
const (
	eraseAccountingByPhoneStmt          = `DELETE FROM radacct WHERE username = ANY($1)`
	eraseAuthLogByPhoneStmt             = `DELETE FROM radpostauth WHERE username = ANY($1)`
	eraseAuthorizationEventsByPhoneStmt = `DELETE FROM authorization_events WHERE username = ANY($1)`
)

// The erasers that must run on this connection are registered from here and only
// from here: `RegisterEraser` in privacy.go registers application-side erasers and
// cannot put one on the retention connection.
func init() {
	registerEraser(Eraser{Table: "radacct", Kinds: []SubjectKind{SubjectMAC, SubjectAccount, SubjectPhone}, Order: 10,
		onRetention: true, Erase: func(ctx context.Context, tx pgx.Tx, s Subject) (int64, error) {
			if s.Kind == SubjectMAC {
				return execCount(ctx, tx, eraseAccountingByMACStmt, s.MACHex)
			}
			if s.Kind == SubjectPhone {
				return execCount(ctx, tx, eraseAccountingByPhoneStmt, s.PhoneSpellings)
			}
			return execCount(ctx, tx, eraseAccountingByUsernameStmt, s.Username, s.SiteID)
		}})
	registerEraser(Eraser{Table: "radpostauth", Kinds: []SubjectKind{SubjectMAC, SubjectAccount, SubjectPhone}, Order: 10,
		onRetention: true, Erase: func(ctx context.Context, tx pgx.Tx, s Subject) (int64, error) {
			if s.Kind == SubjectMAC {
				return execCount(ctx, tx, eraseAuthLogByMACStmt, s.MACHex)
			}
			if s.Kind == SubjectPhone {
				return execCount(ctx, tx, eraseAuthLogByPhoneStmt, s.PhoneSpellings)
			}
			return execCount(ctx, tx, eraseAuthLogByUsernameStmt, s.Username, s.SiteID)
		}})
	registerEraser(Eraser{Table: "authorization_events", Kinds: []SubjectKind{SubjectMAC, SubjectAccount, SubjectPhone}, Order: 10,
		onRetention: true, Erase: func(ctx context.Context, tx pgx.Tx, s Subject) (int64, error) {
			if s.Kind == SubjectMAC {
				return execCount(ctx, tx, eraseAuthorizationEventsByMACStmt, s.MAC)
			}
			if s.Kind == SubjectPhone {
				return execCount(ctx, tx, eraseAuthorizationEventsByPhoneStmt, s.PhoneSpellings)
			}
			return execCount(ctx, tx, eraseAuthorizationEventsByUsernameStmt, s.Username, s.SiteID)
		}})
}

// RetentionEraser performs the retention connection's half of an erasure: every
// registered eraser that runs on foyer_retention, for one subject, in one
// transaction, answering the rows removed per table.
//
// An interface because the connection can live outside the control plane's
// process: `*Retention` is the in-process implementation, and
// `internal/jobs.RemoteClient` asks the maintenance container, which alone holds
// the foyer_retention login, over the backplane. Either way the erasers are this
// package's registry, resolved on the side that runs them.
type RetentionEraser interface {
	EraseRetained(ctx context.Context, s Subject) (map[string]int64, error)
}

// EraseRetained is RetentionEraser on this connection. A nil connection is an
// error, never a panic: a pool that did not open is a reason, not a crash.
func (r *Retention) EraseRetained(ctx context.Context, s Subject) (map[string]int64, error) {
	if r == nil {
		return nil, errors.New("store: the retention connection is not open")
	}
	var on []Eraser
	for _, e := range Erasers(s.Kind) {
		if e.onRetention {
			on = append(on, e)
		}
	}
	return r.erase(ctx, s, on)
}

// erase runs the retention-side erasers for one subject in one transaction, so the
// FreeRADIUS tables are either all erased or none are.
func (r *Retention) erase(ctx context.Context, s Subject, erasers []Eraser) (map[string]int64, error) {
	out := map[string]int64{}
	err := r.s.tx(ctx, func(tx pgx.Tx) error {
		for _, e := range erasers {
			n, err := e.Erase(ctx, tx, s)
			if err != nil {
				return fmt.Errorf("store: erasing from %s: %w", e.Table, err)
			}
			out[e.Table] += n
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
