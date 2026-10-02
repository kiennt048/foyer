package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

// Settings is the maintenance configuration the standard jobs run under: the
// interval, the reaper's two thresholds and the retention batch. It lives in the
// database (`store.MaintenanceRepo`, migration 00036) and is set in the console
// (`updateMaintenanceSettings`), because no operator setting lives in an
// environment variable read once at start.
//
// **Read every cycle, and pushed on write.** Every job refreshes it at the start of
// its run, at most once per `settingsTTL` across all of them, so a second control
// plane -- a blue/green cutover -- picks up an edit within one cycle. The process
// that served the write is told directly (`Apply`), and cmd/foyer then wakes the
// scheduler, so there the edit takes effect at once.
//
// A read that fails keeps the last values it had, and before the first read the
// defaults: a database blip is not a reason to change the schedule.
type Settings struct {
	load   func(context.Context) (store.MaintenanceValues, error)
	logger *slog.Logger
	now    func() time.Time

	mu      sync.Mutex
	cur     store.MaintenanceValues
	fetched time.Time
}

// settingsTTL is how long one read serves every job. Six jobs share one cycle, and
// a read per job per cycle would be six identical queries.
const settingsTTL = 10 * time.Second

// NewSettings reads through load. A nil load serves the defaults forever.
func NewSettings(load func(context.Context) (store.MaintenanceValues, error), logger *slog.Logger) *Settings {
	if logger == nil {
		logger = slog.Default()
	}
	return &Settings{load: load, logger: logger, now: time.Now, cur: store.DefaultMaintenance()}
}

// FromStore reads the settings from the application's database.
func FromStore(st *store.Store) func(context.Context) (store.MaintenanceValues, error) {
	return func(ctx context.Context) (store.MaintenanceValues, error) {
		m, err := st.Maintenance().Get(ctx)
		return m.MaintenanceValues, err
	}
}

// Current is the values in force. No I/O: the scheduler's wait loop calls it.
func (s *Settings) Current() store.MaintenanceValues {
	if s == nil {
		return store.DefaultMaintenance()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Refresh reads the database when the last read is older than settingsTTL.
func (s *Settings) Refresh(ctx context.Context) {
	if s == nil || s.load == nil {
		return
	}
	s.mu.Lock()
	fresh := !s.fetched.IsZero() && s.now().Sub(s.fetched) < settingsTTL
	s.mu.Unlock()
	if fresh {
		return
	}
	v, err := s.load(ctx)
	if err != nil {
		s.logger.Warn("maintenance settings could not be read; keeping the last values", "error", err.Error())
		return
	}
	if bad := v.Validate(); len(bad) > 0 {
		// The CHECKs make this unreachable; a row that somehow passed them is not
		// a schedule to follow.
		s.logger.Error("maintenance settings out of range; keeping the last values", "error", bad[0].Error())
		return
	}
	s.Apply(v)
}

// Apply installs values just written, without waiting for the next read.
func (s *Settings) Apply(v store.MaintenanceValues) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur, s.fetched = v, s.now()
}

// timingFor is the schedule the standard jobs derive from one interval: up to a
// tenth of it as jitter, and a timeout of the interval or a minute, whichever is
// longer (times `timeoutFactor`). **Derived, never set**: an operator who could set a
// timeout shorter than a healthy pass would get a pass cancelled every cycle.
func timingFor(every time.Duration, timeoutFactor int) Timing {
	return Timing{Every: every, Jitter: every / 10, Timeout: time.Duration(timeoutFactor) * max(every, time.Minute)}
}
