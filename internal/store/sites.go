package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kiennt048/foyer/internal/aaa"
)

// SiteRepo owns `sites`.
type SiteRepo struct{ s *Store }

const siteColumns = `id, name, environment, created_at, updated_at`

// List returns every site in the tenant, ordered by name.
//
// Not paginated, and deliberately: a site is a venue, an install has a handful,
// and the console's site switcher needs all of them to render at all.
func (r *SiteRepo) List(ctx context.Context) ([]Site, error) {
	rows, err := r.s.pool.Query(ctx, `SELECT `+siteColumns+` FROM sites ORDER BY name`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []Site
	for rows.Next() {
		var s Site
		if err := rows.Scan(&s.ID, &s.Name, &s.Environment, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, s)
	}
	return out, mapErr(rows.Err())
}

// Get returns one site.
func (r *SiteRepo) Get(ctx context.Context, id string) (*Site, error) {
	var s Site
	err := r.s.pool.QueryRow(ctx, `SELECT `+siteColumns+` FROM sites WHERE id = $1`, id).
		Scan(&s.ID, &s.Name, &s.Environment, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &s, nil
}

// ErrSiteExists is the refusal that makes "one venue per install" a rule the
// database applies rather than a sentence in a decision record.
//
// One venue per install is a deliberate design decision. The
// reasoning ends at the protocol: `nas.nasname` is globally unique because
// FreeRADIUS's client list is one flat namespace, so two venues on private-range
// networks sharing `192.168.1.1` can never both be projected. A second customer is
// a second installation.
var ErrSiteExists = errors.New("store: this deployment already has a site")

// CreateFirst adds the deployment's first site, and refuses if it has one.
//
// **One statement, not a count followed by an insert.** The read-then-write form is
// the shape `docs/rules/security.md` §9 rejects for voucher claims, and for the
// same reason: two concurrent calls both read zero and both insert. The window is
// small and an install-time operation makes it look harmless, which is exactly what
// makes it the kind of race nobody ever reproduces.
//
// `WHERE NOT EXISTS (SELECT 1 FROM sites)` runs under the row-level policy, so it
// sees this tenant's sites and no other tenant's. That is the intended reading:
// the seam stays, and the ruling is about a venue per install.
//
// `Create` above is untouched and is still what test seeding and migrations use. A
// single-row CHECK on the table would have been the tempting shape and it is the
// wrong one — the schema permitting many sites is the insurance the design chose
// to keep, and it is what lets a store test seed two of them to prove a scope.
func (r *SiteRepo) CreateFirst(ctx context.Context, s *Site) error {
	if s.Environment == "" {
		s.Environment = "live"
	}
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO sites (id, name, environment)
		SELECT $1, $2, $3
		WHERE NOT EXISTS (SELECT 1 FROM sites)
		RETURNING created_at, updated_at`,
		s.ID, s.Name, s.Environment).Scan(&s.CreatedAt, &s.UpdatedAt)
	if errors.Is(mapErr(err), ErrNotFound) {
		// No row came back, so the WHERE NOT EXISTS was false. pgx reports that as
		// ErrNoRows, which mapErr turns into ErrNotFound — a sentinel that would
		// reach a handler as a 404 for a creation, which is nonsense. Translated
		// here, at the only place that knows what the absent row means.
		return ErrSiteExists
	}
	return mapErr(err)
}

// Create adds a site. The contract's `createSite` reaches it, and so do tests: a
// store with no way to create its root aggregate cannot be tested.
func (r *SiteRepo) Create(ctx context.Context, s *Site) error {
	if s.Environment == "" {
		s.Environment = "live"
	}
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO sites (id, name, environment)
		VALUES ($1, $2, $3)
		RETURNING created_at, updated_at`,
		s.ID, s.Name, s.Environment).Scan(&s.CreatedAt, &s.UpdatedAt)
	return mapErr(err)
}

// Rename updates a site's display name.
func (r *SiteRepo) Rename(ctx context.Context, id, name string) error {
	tag, err := r.s.pool.Exec(ctx,
		`UPDATE sites SET name = $2, updated_at = now() WHERE id = $1`, id, name)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// The per-site failure model.
// ---------------------------------------------------------------------------

// MinGraceSeconds is the floor the CHECK constraint on `sites` enforces.
//
// `aaa` has a ceiling (MaxGraceSeconds) but no floor, because nothing there can
// produce a value below the default. The database can -- an operator typing into a
// form -- so the floor lives on this side of the boundary.
const MinGraceSeconds = 60

// SiteFailurePolicies implements aaa.FailurePolicyStore.
//
// Every site at once, because the caller is the FreeRADIUS config generator rather
// than the request path: the answer is baked into unlang at generation time, not
// looked up per Access-Request. That is also why nothing here caches it.
//
// A row whose mode fails Valid() is not repaired and not skipped: it is returned as
// stored, and `aaa` resolves an unparseable mode to fail-closed. The CHECK
// constraint means such a row cannot exist, and the two layers agreeing to fail in
// the same direction anyway is the point.
func (r *SiteRepo) SiteFailurePolicies(ctx context.Context) ([]aaa.SiteFailurePolicy, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT id, guest_policy_failure_mode, guest_policy_failure_grace_seconds
		FROM sites ORDER BY id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	out := []aaa.SiteFailurePolicy{}
	for rows.Next() {
		var p aaa.SiteFailurePolicy
		var mode string
		if err := rows.Scan(&p.SiteID, &mode, &p.GuestGraceSeconds); err != nil {
			return nil, mapErr(err)
		}
		p.GuestFailureMode = aaa.FailureMode(mode)
		out = append(out, p)
	}
	return out, mapErr(rows.Err())
}

// SetFailurePolicy records the operator's choice for one site.
//
// The mode is validated here as well as by the CHECK constraint, so a bad value is
// a named refusal rather than a constraint violation surfacing as a 500. The grace
// seconds are clamped rather than refused, because `aaa` clamps them too and a form
// that rejects 30 outright teaches less than one that shows the applied minimum.
func (r *SiteRepo) SetFailurePolicy(ctx context.Context, siteID string, mode aaa.FailureMode, graceSeconds int) error {
	if !mode.Valid() {
		return fmt.Errorf("store: %q is not a failure mode", mode)
	}
	switch {
	case graceSeconds < MinGraceSeconds:
		graceSeconds = MinGraceSeconds
	case graceSeconds > aaa.MaxGraceSeconds:
		graceSeconds = aaa.MaxGraceSeconds
	}

	tag, err := r.s.pool.Exec(ctx, `
		UPDATE sites
		SET guest_policy_failure_mode = $2, guest_policy_failure_grace_seconds = $3, updated_at = now()
		WHERE id = $1`, siteID, string(mode), graceSeconds)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// The site's calendar.
// ---------------------------------------------------------------------------

// TimeZone is the IANA zone a site counts its days in (migration 00033), and when
// an operator last changed it (migration 00044) -- nil for a zone nobody has set,
// which is every site's until somebody does. The name is stored as the operator
// chose it and validated by the caller against the zone database the process has;
// the column's CHECK bounds only its shape.
func (r *SiteRepo) TimeZone(ctx context.Context, siteID string) (zone string, changedAt *time.Time, err error) {
	err = r.s.pool.QueryRow(ctx, `SELECT time_zone, time_zone_changed_at FROM sites WHERE id = $1`, siteID).
		Scan(&zone, &changedAt)
	return zone, changedAt, mapErr(err)
}

// SetTimeZone records the site's zone. ErrNotFound for an unknown site.
//
// `time_zone_changed_at` moves only when the zone does: storing the zone a site
// already has is not a change of calendar, and a timestamp that moved on it would
// say one happened.
func (r *SiteRepo) SetTimeZone(ctx context.Context, siteID, zone string) error {
	tag, err := r.s.pool.Exec(ctx, `
		UPDATE sites
		   SET time_zone_changed_at = CASE WHEN time_zone IS DISTINCT FROM $2
		                                   THEN now() ELSE time_zone_changed_at END,
		       time_zone = $2,
		       updated_at = now()
		 WHERE id = $1`, siteID, zone)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
