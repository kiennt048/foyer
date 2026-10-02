package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// MaintenanceRepo owns `maintenance_settings` (migration 00036): how often the
// control plane's maintenance jobs run and how much one pass may do.
//
// ── why these four and not more ──────────────────────────────────────────────
//
// The rule is that every operator setting is set in the console, never in the
// environment. PacketFence's maintenance tasks
// (`conf/pfcron.conf.defaults`, edited in its admin UI under *Maintenance*) are
// the survey: each task carries an interval, a batch size and a timeout, and its
// admin guide warns that a timeout shorter than the task's run leaves the task
// unfinished without saying so. Foyer takes the interval and the batch from that,
// adds the reaper's two decisions -- how long a silent session lives and the most
// one pass may close -- and **derives** the timeout and the jitter from the
// interval (internal/jobs), so the combination PacketFence warns about cannot be
// chosen.
//
// Each bound is here, once, and the migration's CHECK repeats it as a backstop.
type MaintenanceRepo struct{ s *Store }

// Maintenance is constructed per call, so this aggregate adds a file rather than a
// field to the shared Store struct.
func (s *Store) Maintenance() *MaintenanceRepo { return &MaintenanceRepo{s: s} }

// MaintenanceValues is what an operator sets.
type MaintenanceValues struct {
	IntervalSeconds         int
	StaleAfterMissedUpdates int
	StaleCeilingPerPass     int
	RetentionBatchSize      int
}

// Interval is IntervalSeconds as a duration.
func (v MaintenanceValues) Interval() time.Duration {
	return time.Duration(v.IntervalSeconds) * time.Second
}

// MaintenanceSettings is the row in force: the values, and who saved them.
type MaintenanceSettings struct {
	MaintenanceValues
	IsDefault bool
	UpdatedAt *time.Time
	UpdatedBy *string
}

// MaintenanceLimit bounds one value.
type MaintenanceLimit struct{ Min, Max, Default int }

// MaintenanceLimits are the bounds and defaults, by the contract's field name.
//
//   - intervalSeconds: 5 minutes by default, the floor of Acct-Interim-Interval
//     (docs/rules/radius.md). At least a minute, at most a day: a retention sweep that runs
//     once a week is a retention period a week longer than the one promised.
//   - staleAfterMissedUpdates: 4 by default (session.ReaperConfig). **At least 3**,
//     because the console marks a session orphaned at 2 and a reaper that closes at
//     the display threshold hides the symptom an operator finds a bad NAS by.
//   - staleCeilingPerPass: 200 by default. Never zero: zero disables the reaper's
//     ceiling, and that is not a switch the console offers.
//   - retentionBatchSize: 500 by default; PacketFence uses 100 per task per minute.
var MaintenanceLimits = map[string]MaintenanceLimit{
	"intervalSeconds":         {Min: 60, Max: 86400, Default: 300},
	"staleAfterMissedUpdates": {Min: 3, Max: 48, Default: 4},
	"staleCeilingPerPass":     {Min: 1, Max: 100000, Default: 200},
	"retentionBatchSize":      {Min: 50, Max: 10000, Default: 500},
}

// DefaultMaintenance is every value at its default.
func DefaultMaintenance() MaintenanceValues {
	return MaintenanceValues{
		IntervalSeconds:         MaintenanceLimits["intervalSeconds"].Default,
		StaleAfterMissedUpdates: MaintenanceLimits["staleAfterMissedUpdates"].Default,
		StaleCeilingPerPass:     MaintenanceLimits["staleCeilingPerPass"].Default,
		RetentionBatchSize:      MaintenanceLimits["retentionBatchSize"].Default,
	}
}

// Fields pairs each value with its contract field name, in a fixed order.
func (v MaintenanceValues) Fields() []struct {
	Name  string
	Value int
} {
	return []struct {
		Name  string
		Value int
	}{
		{"intervalSeconds", v.IntervalSeconds},
		{"staleAfterMissedUpdates", v.StaleAfterMissedUpdates},
		{"staleCeilingPerPass", v.StaleCeilingPerPass},
		{"retentionBatchSize", v.RetentionBatchSize},
	}
}

// ErrMaintenanceOutOfRange names the first field outside its bounds.
var ErrMaintenanceOutOfRange = errors.New("store: maintenance setting out of range")

// MaintenanceRangeError says which field and which bound.
type MaintenanceRangeError struct {
	Field string
	Limit MaintenanceLimit
	Value int
}

func (e *MaintenanceRangeError) Error() string {
	return fmt.Sprintf("%v: %s must be between %d and %d (got %d)", ErrMaintenanceOutOfRange,
		e.Field, e.Limit.Min, e.Limit.Max, e.Value)
}

func (e *MaintenanceRangeError) Unwrap() error { return ErrMaintenanceOutOfRange }

// Validate returns every field outside its bounds.
func (v MaintenanceValues) Validate() []*MaintenanceRangeError {
	var out []*MaintenanceRangeError
	for _, f := range v.Fields() {
		l := MaintenanceLimits[f.Name]
		if f.Value < l.Min || f.Value > l.Max {
			out = append(out, &MaintenanceRangeError{Field: f.Name, Limit: l, Value: f.Value})
		}
	}
	return out
}

// Get answers the settings in force: the stored row, or the defaults.
func (r *MaintenanceRepo) Get(ctx context.Context) (MaintenanceSettings, error) {
	var m MaintenanceSettings
	var at time.Time
	err := r.s.pool.QueryRow(ctx, `
		SELECT interval_seconds, stale_after_missed_updates, stale_ceiling_per_pass,
		       retention_batch_size, updated_by, updated_at
		  FROM maintenance_settings`).
		Scan(&m.IntervalSeconds, &m.StaleAfterMissedUpdates, &m.StaleCeilingPerPass,
			&m.RetentionBatchSize, &m.UpdatedBy, &at)
	if err != nil {
		if err = mapErr(err); errors.Is(err, ErrNotFound) {
			return MaintenanceSettings{MaintenanceValues: DefaultMaintenance(), IsDefault: true}, nil
		}
		return MaintenanceSettings{}, err
	}
	m.UpdatedAt = &at
	return m, nil
}

// Set replaces the settings. A value out of range is refused before anything is
// written, with the first offending field.
func (r *MaintenanceRepo) Set(ctx context.Context, v MaintenanceValues, actor string) (MaintenanceSettings, error) {
	if bad := v.Validate(); len(bad) > 0 {
		return MaintenanceSettings{}, bad[0]
	}
	var by *string
	if actor != "" {
		by = &actor
	}
	if _, err := r.s.pool.Exec(ctx, `
		INSERT INTO maintenance_settings
			(interval_seconds, stale_after_missed_updates, stale_ceiling_per_pass,
			 retention_batch_size, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (tenant_id) DO UPDATE SET
			interval_seconds           = EXCLUDED.interval_seconds,
			stale_after_missed_updates = EXCLUDED.stale_after_missed_updates,
			stale_ceiling_per_pass     = EXCLUDED.stale_ceiling_per_pass,
			retention_batch_size       = EXCLUDED.retention_batch_size,
			updated_by                 = EXCLUDED.updated_by,
			updated_at                 = now()`,
		v.IntervalSeconds, v.StaleAfterMissedUpdates, v.StaleCeilingPerPass, v.RetentionBatchSize, by); err != nil {
		return MaintenanceSettings{}, mapErr(err)
	}
	return r.Get(ctx)
}
