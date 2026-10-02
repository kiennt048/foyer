package session

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kiennt048/foyer/internal/redact"
)

// The stale-session reaper: the one place in Go that writes to `radacct`.
//
// The failure it exists for is the classic one, and `docs/rules/radius.md` is
// explicit that it is not to be rediscovered: a NAS reboots, never sends the
// Acct-Stop for the sessions it had open, and those rows stay open forever. The
// console shows a venue with four hundred people online at 3 a.m.; concurrency
// limits count sessions that ended last week; and every figure derived from "how
// many are connected" is wrong in the same direction, permanently.
//
// Three properties make this an audited exception rather than a hole:
//
//   - **Its whole write surface is reaper.sql**, one statement, embedded. This
//     file composes no SQL and there is nothing else in the package that writes.
//   - **PostgreSQL enforces the boundary independently.** It connects as
//     `foyer_reaper`, which holds SELECT plus column-level UPDATE on exactly four
//     columns and has no privilege at all on `radpostauth`. A fifth column is a
//     conversation with whoever owns migration 00002, not an edit here.
//   - **It logs every row it closes.** A silent reaper and a bug that closes live
//     sessions look identical from outside.

//go:embed reaper.sql
var reapStatement string

// ErrReaperRefused is returned when a pass would close more sessions than
// MaxPerPass allows. See that field.
var ErrReaperRefused = errors.New("session: reaper refused a pass larger than its ceiling")

// ReaperConfig bounds one pass.
type ReaperConfig struct {
	// SilenceMultiplier is how many of a session's own reporting intervals must
	// pass in silence before it is closed. Default 4.
	//
	// Four, and not the two the console uses to *mark* a session orphaned. Those
	// are deliberately different numbers: a session that is closed the instant it
	// becomes orphaned is a session no operator ever sees as orphaned, and
	// "sessions keep going orphaned on that AP" is the signal that a NAS is
	// misbehaving. Reaping at the display threshold removes the symptom and keeps
	// the disease.
	SilenceMultiplier int

	// IntervalFloorSeconds is the shortest reporting interval believed. Default
	// 300: Acct-Interim-Interval has a widely-implemented five-minute floor
	// (`docs/rules/radius.md`), so a NAS claiming ten seconds is not evidence
	// that ten seconds of silence means anything.
	//
	// With the defaults a session is closed after 20 minutes of silence.
	IntervalFloorSeconds int

	// BatchLimit caps one statement. Default 500.
	BatchLimit int

	// MaxPerPass refuses the whole pass when it would close more than this.
	// Default 200; zero disables the ceiling.
	//
	// A site-wide outage produces exactly the same query result as a bug in the
	// silence arithmetic, and the reaper cannot tell them apart. When the number is
	// large enough to be either, refusing and alarming is right: the sessions are
	// already over, so nothing is lost by closing them an hour later with a human
	// watching, and everything is lost by closing four hundred live ones.
	MaxPerPass int

	// TerminateCause is written to `acctterminatecause`. Default
	// "Foyer-Stale-Reaped".
	//
	// Deliberately not an RFC 2866 §5.10 value. Every one of those --
	// `NAS-Reboot`, `Lost-Carrier`, `NAS-Error` -- is a claim about what the NAS
	// did, and the reaper knows only that the NAS went quiet. A cause that is
	// obviously not from a NAS is what lets a reader, and stock FreeRADIUS
	// tooling reading this table after Foyer is gone, tell the two apart.
	TerminateCause string

	// DryRun runs the selection and logs what it would close, changing nothing.
	DryRun bool
}

const (
	defaultSilenceMultiplier = 4
	defaultIntervalFloor     = 300
	defaultBatchLimit        = 500
	defaultMaxPerPass        = 200
	defaultTerminateCause    = "Foyer-Stale-Reaped"
)

func (c ReaperConfig) withDefaults() ReaperConfig {
	if c.SilenceMultiplier <= 0 {
		c.SilenceMultiplier = defaultSilenceMultiplier
	}
	if c.IntervalFloorSeconds <= 0 {
		c.IntervalFloorSeconds = defaultIntervalFloor
	}
	if c.BatchLimit <= 0 {
		c.BatchLimit = defaultBatchLimit
	}
	if c.MaxPerPass < 0 {
		c.MaxPerPass = 0
	} else if c.MaxPerPass == 0 {
		c.MaxPerPass = defaultMaxPerPass
	}
	if c.TerminateCause == "" {
		c.TerminateCause = defaultTerminateCause
	}
	return c
}

// SilenceThreshold is how long a session with the given reporting interval must be
// quiet before this configuration closes it. Exposed because the admin console has
// to be able to say it out loud beside an orphaned session.
func (c ReaperConfig) SilenceThreshold(acctInterval int) time.Duration {
	c = c.withDefaults()
	if acctInterval < c.IntervalFloorSeconds {
		acctInterval = c.IntervalFloorSeconds
	}
	return time.Duration(c.SilenceMultiplier*acctInterval) * time.Second
}

// ReaperDB is the narrowest thing the reaper needs: one query.
//
// It must be a pool connecting as `foyer_reaper`. Handing it the application's pool
// would not silently work -- `foyer_app` has SELECT only, and PostgreSQL refuses
// the UPDATE -- which is the shape this whole arrangement is for.
type ReaperDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ReapedSession is one row the reaper closed. It is what gets logged, and what the
// caller may audit.
type ReapedSession struct {
	AcctUniqueID     string
	AcctSessionID    string
	Username         *string
	NASIPAddress     string
	CallingStationID string
	StartTime        *time.Time
	StopTime         *time.Time
	SessionSeconds   *int64
	// SilentFor is how long nothing had been heard when the pass ran.
	SilentFor time.Duration
}

// Reaper closes sessions no NAS will ever send an Acct-Stop for.
type Reaper struct {
	db     ReaperDB
	cfg    atomic.Pointer[ReaperConfig]
	logger *slog.Logger
}

// NewReaper builds a reaper over a pool that must authenticate as `foyer_reaper`.
func NewReaper(db ReaperDB, cfg ReaperConfig, logger *slog.Logger) *Reaper {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Reaper{db: db, logger: logger}
	r.Configure(cfg)
	return r
}

// Config returns the effective configuration, defaults filled in.
func (r *Reaper) Config() ReaperConfig { return *r.cfg.Load() }

// Configure replaces the configuration the next pass uses; a pass already running
// keeps the one it started with. Zero fields take their defaults, as in NewReaper.
//
// It exists for the admin-configurability rule, under which every operator setting
// is set in the console and takes effect without a restart: the silence multiplier
// and the per-pass ceiling are operator settings, stored in the database
// (`store.MaintenanceRepo`) and read by internal/jobs every cycle, and a reaper
// whose configuration was fixed at construction would need a restart to honour an
// edit made in the console.
func (r *Reaper) Configure(cfg ReaperConfig) {
	c := cfg.withDefaults()
	r.cfg.Store(&c)
}

// Reap runs one pass and returns the sessions it closed.
//
// On DryRun it returns what it would have closed and writes nothing. On a pass
// larger than MaxPerPass it returns ErrReaperRefused, having written nothing --
// see MaxPerPass for why that is the safe direction.
func (r *Reaper) Reap(ctx context.Context) ([]ReapedSession, error) {
	if r.db == nil {
		return nil, errors.New("session: reaper has no database")
	}
	// One configuration for the whole pass: the ceiling is checked against the
	// same predicate the write uses, and an edit landing between the two must not
	// make them disagree.
	cfg := r.Config()

	candidates, err := r.candidates(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	if cfg.MaxPerPass > 0 && len(candidates) > cfg.MaxPerPass {
		// Loud, and nothing written. The sessions are already over; closing them
		// an hour later with somebody watching costs nothing, and closing four
		// hundred live ones costs a venue.
		r.logger.Error("stale-session reaper refused a pass and wrote nothing",
			"candidates", len(candidates),
			"ceiling", cfg.MaxPerPass,
			"hint", "a site-wide outage and a bug in the silence arithmetic produce the same result here")
		return nil, fmt.Errorf("%w: %d candidates against a ceiling of %d",
			ErrReaperRefused, len(candidates), cfg.MaxPerPass)
	}

	if cfg.DryRun {
		for _, s := range candidates {
			r.logger.Info("stale-session reaper would close a session",
				"acctUniqueId", s.AcctUniqueID,
				"nas", s.NASIPAddress,
				// Masked: a log line is a copy no erasure reaches (internal/redact).
				"callingStationId", redact.MAC(s.CallingStationID),
				"silentFor", s.SilentFor.String(),
				"dryRun", true)
		}
		return candidates, nil
	}

	closed, err := r.execute(ctx, cfg)
	if err != nil {
		return nil, err
	}
	for _, s := range closed {
		// Every row, not a count. A reaper whose log says "closed 37" and a bug
		// that closed 37 live sessions are the same log line.
		r.logger.Info("stale-session reaper closed a session with no Acct-Stop",
			"acctUniqueId", s.AcctUniqueID,
			"acctSessionId", s.AcctSessionID,
			"nas", s.NASIPAddress,
			"callingStationId", redact.MAC(s.CallingStationID),
			"stopTime", s.StopTime,
			"sessionSeconds", s.SessionSeconds,
			"terminateCause", cfg.TerminateCause)
	}
	return closed, nil
}

// candidates is the read half, run before every pass so the ceiling is checked
// against the same predicate the write uses.
//
// It is a separate statement rather than a CTE on purpose: a ceiling evaluated
// inside the statement it is meant to stop is a ceiling that has already written
// the rows by the time it fires.
const candidateQuery = `
	SELECT acctuniqueid, acctsessionid, username, host(nasipaddress),
	       coalesce(callingstationid, ''), acctstarttime,
	       extract(epoch FROM now() - coalesce(acctupdatetime, acctstarttime))::bigint
	FROM radacct
	WHERE acctstoptime IS NULL
	  AND acctstarttime IS NOT NULL
	  AND coalesce(acctupdatetime, acctstarttime)
	      < now() - (($1::int * greatest(coalesce(acctinterval, $2::int), $2::int))::text
	                 || ' seconds')::interval
	ORDER BY coalesce(acctupdatetime, acctstarttime)
	LIMIT $3::int`

func (r *Reaper) candidates(ctx context.Context, cfg ReaperConfig) ([]ReapedSession, error) {
	rows, err := r.db.Query(ctx, candidateQuery,
		cfg.SilenceMultiplier, cfg.IntervalFloorSeconds, cfg.BatchLimit)
	if err != nil {
		return nil, fmt.Errorf("session: reaper could not scan for stale sessions: %w", err)
	}
	defer rows.Close()

	var out []ReapedSession
	for rows.Next() {
		var s ReapedSession
		var silent int64
		if err := rows.Scan(&s.AcctUniqueID, &s.AcctSessionID, &s.Username,
			&s.NASIPAddress, &s.CallingStationID, &s.StartTime, &silent); err != nil {
			return nil, fmt.Errorf("session: reaper could not read a candidate: %w", err)
		}
		s.SilentFor = time.Duration(silent) * time.Second
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Reaper) execute(ctx context.Context, cfg ReaperConfig) ([]ReapedSession, error) {
	rows, err := r.db.Query(ctx, reapStatement,
		cfg.TerminateCause, cfg.SilenceMultiplier,
		cfg.IntervalFloorSeconds, cfg.BatchLimit)
	if err != nil {
		return nil, fmt.Errorf("session: reaper could not close stale sessions: %w", err)
	}
	defer rows.Close()

	var out []ReapedSession
	for rows.Next() {
		var s ReapedSession
		if err := rows.Scan(&s.AcctUniqueID, &s.AcctSessionID, &s.Username,
			&s.NASIPAddress, &s.CallingStationID,
			&s.StartTime, &s.StopTime, &s.SessionSeconds); err != nil {
			return nil, fmt.Errorf("session: reaper could not read a closed session: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Run reaps on a ticker until the context is cancelled.
//
// Errors are logged and the loop continues: a reaper that stops on the first
// failure is a reaper that is off, and nothing notices it is off until the console
// has been wrong for a month.
func (r *Reaper) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 5 * time.Minute
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	cfg := r.Config()
	r.logger.Info("stale-session reaper started",
		"every", every.String(),
		"silenceMultiplier", cfg.SilenceMultiplier,
		"intervalFloorSeconds", cfg.IntervalFloorSeconds,
		"threshold", cfg.SilenceThreshold(cfg.IntervalFloorSeconds).String(),
		"maxPerPass", cfg.MaxPerPass,
		"dryRun", cfg.DryRun)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("stale-session reaper stopped")
			return
		case <-ticker.C:
			if _, err := r.Reap(ctx); err != nil && !errors.Is(err, context.Canceled) {
				r.logger.Error("stale-session reaper pass failed", "error", err.Error())
			}
		}
	}
}
