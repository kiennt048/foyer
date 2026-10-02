package main

import (
	"context"
	"log/slog"

	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/jobs"
	"github.com/kiennt048/foyer/internal/session"
	"github.com/kiennt048/foyer/internal/store"
)

// maintenance is the in-process scheduler and the two extra pools its jobs need.
//
// Without it this process would start three listeners and nothing else:
// `ReapSessions`, `DeleteExpired`, `ExpirePast` and `session.NewReaper` would have
// no caller outside their tests. See internal/jobs for the design and the survey.
//
// ── two more pools, each as its own role ─────────────────────────────────────
//
// The reaper must run as `foyer_reaper` and retention as `foyer_retention`
// (migrations 00001, 00020): handing either the application's pool would not work
// silently -- PostgreSQL refuses the write -- which is the arrangement's point. With
// one FOYER_DATABASE_URL both are the same login plus a SET ROLE; a deployment that
// gives the roles logins of their own supplies FOYER_REAPER_DATABASE_URL and
// FOYER_RETENTION_DATABASE_URL (each honours the _FILE convention, since a URL
// carries a password).
//
// **A pool that cannot open does not stop the process.** This process also serves
// the Policy API every Wi-Fi sign-in asks, and refusing to start over a sweep would
// turn a retention misconfiguration into a venue with no network. Instead the job is
// registered anyway and fails every cycle with the reason, at ERROR -- a sweep that
// is off must not look like one that ran. The erasure operation answers with a
// problem for the same reason rather than disappearing.
//
// ── the settings are the console's ───────────────────────────────────────────
//
// The interval, the reaper's two thresholds and the retention batch are stored in
// the database (`store.MaintenanceRepo`) and set with `updateMaintenanceSettings`
// -- an operator never edits an environment variable. There is no
// `FOYER_MAINTENANCE_INTERVAL` fallback read at start: a fallback that can
// disagree with the console is a second setting. The jobs re-read the row every
// cycle, and `SettingsChanged` -- which the API adapter calls after a write --
// installs the new values and wakes the
// scheduler, so an edit is in force at once.
type maintenance struct {
	sched     *jobs.Scheduler
	settings  *jobs.Settings
	reaperDB  *store.Store
	retention *store.Retention
}

// openMaintenance opens the reaper's and the retention role's pools and builds the
// scheduler. A pool that will not open is carried into the jobs as their reason for
// failing, never a refusal to start.
func openMaintenance(ctx context.Context, cfg config.Config, app *store.Store) (*maintenance, error) {
	logger := slog.Default()
	m := &maintenance{settings: jobs.NewSettings(jobs.FromStore(app), logger)}

	deps := jobs.Deps{Store: app, Logger: logger, Settings: m.settings}

	reaperURL, role, err := rolePoolURL(cfg.ReaperDatabase, cfg.DatabaseURL, store.ReaperRole)
	if err == nil {
		m.reaperDB, err = store.Open(ctx, store.Config{URL: reaperURL, Role: role, MaxConns: 2})
	}
	if err != nil {
		logger.Error("the stale-session reaper's pool did not open; the job will fail every cycle until it does",
			"role", store.ReaperRole, "error", err.Error())
		deps.ReaperErr = err
	} else {
		deps.Reaper = session.NewReaper(m.reaperDB.Pool(), session.ReaperConfig{}, logger)
	}

	retURL, role, err := rolePoolURL(cfg.RetentionDatabase, cfg.DatabaseURL, store.RetentionRole)
	if err == nil {
		m.retention, err = store.OpenRetention(ctx, store.Config{URL: retURL, Role: role})
	}
	if err != nil {
		logger.Error("the retention connection did not open; retention and erasure will fail until it does",
			"role", store.RetentionRole, "error", err.Error())
		deps.RetentionErr = err
	} else {
		deps.Retention = m.retention
	}

	m.sched = buildScheduler(deps, logger)
	return m, nil
}

// buildScheduler registers exactly the standard jobs. Split out so the wiring is
// testable without a database.
func buildScheduler(deps jobs.Deps, logger *slog.Logger) *jobs.Scheduler {
	s := jobs.New(jobs.Options{Logger: logger})
	for _, j := range jobs.Standard(deps) {
		if err := s.Add(j); err != nil {
			// Standard's own list colliding with itself is a programming error.
			panic(err)
		}
	}
	return s
}

// rolePoolURL picks the connection for one role: its own URL if the deployment
// supplied one (the login *is* the role, so no SET ROLE), otherwise the
// application's URL and a SET ROLE.
//
// own is what config.Load read from FOYER_<ROLE>_DATABASE_URL(_FILE); its read
// error is this pool's reason for failing, never a refusal to start.
func rolePoolURL(own config.SecretValue, fallback, role string) (url, setRole string, err error) {
	if own.Err != nil {
		return "", "", own.Err
	}
	if own.Value != "" {
		return own.Value, store.NoRole, nil
	}
	return fallback, role, nil
}

// SettingsChanged installs maintenance settings an operator just saved and wakes
// every waiting job to reschedule against them. The API adapter calls it after a
// successful `updateMaintenanceSettings`; a nil receiver (no maintenance) is a no-op.
func (m *maintenance) SettingsChanged(v store.MaintenanceValues) {
	if m == nil {
		return
	}
	m.settings.Apply(v)
	m.sched.Reschedule()
}

// Retention is the retention connection, or nil when it did not open.
func (m *maintenance) Retention() *store.Retention {
	if m == nil {
		return nil
	}
	return m.retention
}

func (m *maintenance) Start(ctx context.Context) {
	// nil when the jobs are the maintenance container's (maintenanceFor): run hands
	// it to serveUntilSignal as a starter, where a nil *maintenance is a non-nil
	// interface, so the nil check has to be here.
	if m == nil {
		return
	}
	m.sched.Start(ctx)
}

// Stop cancels any pass in progress, waits for every job, then closes the pools.
func (m *maintenance) Stop() {
	if m == nil {
		return
	}
	m.sched.Stop()
	if m.reaperDB != nil {
		m.reaperDB.Close()
	}
	m.retention.Close()
}
