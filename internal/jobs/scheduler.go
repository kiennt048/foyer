// Package jobs is the control plane's in-process maintenance scheduler.
//
// ── why it exists ────────────────────────────────────────────────────────────
//
// Four cleanup functions need a caller outside their tests:
// `GrantRepo.ReapSessions`, `OperatorSessionRepo.DeleteExpired`,
// `VoucherRepo.ExpirePast` and `session.NewReaper`. Without one,
// `portal_sessions` -- writable by anyone in radio range without signing in --
// grows without bound, expired vouchers never change state, expired console
// sessions are never removed, and a NAS that reboots leaves sessions open forever.
//
// ── why in-process, and not cron or a sidecar ────────────────────────────────
//
// The deployment schedules nothing -- no timer, no cron entry -- in `deploy/` (see
// `cmd/foyer`'s comment on certificate rotation). PacketFence runs its maintenance
// in `pfcron`, a daemon of its own with one schedule per task
// (conf/pfcron.conf.defaults); authentik runs its expiry cleanup
// (`clean_expired_models`) as a scheduled task in its worker, a container separate
// from its server. Both are second processes. Foyer is deliberately one ("three
// listeners, one database, one process" -- cmd/foyer), and a second daemon is a
// second thing to deploy, monitor and keep in step with the schema, so the schedule
// lives here: a goroutine per job in the process that already holds the database
// pools, stopped by the same signal that stops the listeners.
//
// Properties, each tested in scheduler_test.go against a fake clock:
//
//   - every job runs once shortly after start (a restarted box does not wait a full
//     interval to sweep), then every `Every` plus up to `Jitter`;
//   - a job never overlaps itself: the next wait starts when the run ends;
//   - a failure or a panic is logged and counted, and the job keeps its schedule --
//     a job that stops on the first error is a job that is off, and nothing
//     notices until the table is full;
//   - `Stop` cancels the running pass through its context and waits for every
//     goroutine to return.
//
// ── metrics ──────────────────────────────────────────────────────────────────
//
// The jobs have no metrics series: no Prometheus client, no expvar endpoint, and no
// new dependency to provide one. So the counters are in-process -- `Stats()` -- and
// every run writes one structured log line carrying the same figures. The retention
// sweep also records its outcome in the database, which is what the console shows.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

// Clock is the scheduler's only notion of time, so tests can drive it.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Job is one periodic task.
type Job struct {
	// Name appears in every log line and in Stats.
	Name string
	// Every is the interval between the end of one run and the start of the next.
	Every time.Duration
	// Jitter is the most added at random to each wait, so two control planes during
	// a blue/green cutover do not sweep in lockstep, and a fleet restarted together
	// does not stampede one database.
	Jitter time.Duration
	// Timeout bounds one run. Zero means Every.
	Timeout time.Duration
	// Run does the work and reports how many rows it affected.
	Run func(ctx context.Context) (affected int64, err error)
	// Tune, when set, is asked for the timing in force before every wait and every
	// run, and wins over the three fields above whenever its Every is positive. It
	// must not do I/O: it is called from the wait loop. It is how an interval an
	// operator changes in the console reaches a job that is already running
	// (Standard's jobs read `store.MaintenanceRepo` through Settings).
	Tune func() Timing
}

// Timing is how one job is scheduled.
type Timing struct {
	Every, Jitter, Timeout time.Duration
}

// Stats is what a job has done since the scheduler started.
type Stats struct {
	Runs, Failures uint64
	// Interrupted counts passes Stop cancelled; they are neither failures nor
	// successes.
	Interrupted   uint64
	TotalAffected int64
	LastAffected  int64
	LastStart     time.Time
	LastDuration  time.Duration
	// LastError is the last failure's text, cleared by the next success.
	LastError string
}

// Options configures a Scheduler. Every field is optional.
type Options struct {
	Clock  Clock
	Logger *slog.Logger
	// Rand returns a value in [0, n); it exists so jitter is deterministic in tests.
	Rand func(n int64) int64
	// InitialDelay is how long after Start the first run of each job begins, before
	// jitter. Default one second: long enough for the listeners to bind first.
	InitialDelay time.Duration
}

// Scheduler runs jobs until stopped.
type Scheduler struct {
	clock   Clock
	logger  *slog.Logger
	rand    func(int64) int64
	initial time.Duration

	mu      sync.Mutex
	jobs    []Job
	stats   map[string]*Stats
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	// wake is closed by Reschedule and replaced, so every waiting job sees it.
	wake chan struct{}
}

// New builds a scheduler with no jobs.
func New(o Options) *Scheduler {
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Rand == nil {
		o.Rand = rand.Int64N
	}
	if o.InitialDelay < 0 {
		o.InitialDelay = 0
	} else if o.InitialDelay == 0 {
		o.InitialDelay = time.Second
	}
	return &Scheduler{clock: o.Clock, logger: o.Logger, rand: o.Rand, initial: o.InitialDelay,
		stats: map[string]*Stats{}, wake: make(chan struct{})}
}

// ErrStarted is returned by Add after Start.
var ErrStarted = errors.New("jobs: the scheduler has already started")

// Add registers a job. It must be called before Start.
func (s *Scheduler) Add(j Job) error {
	if j.Name == "" || j.Run == nil || (j.Every <= 0 && j.Tune == nil) {
		return fmt.Errorf("jobs: a job needs a name, a Run and a positive interval (got %q, every %s)", j.Name, j.Every)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return ErrStarted
	}
	if _, dup := s.stats[j.Name]; dup {
		return fmt.Errorf("jobs: %q is registered twice", j.Name)
	}
	s.jobs = append(s.jobs, j)
	s.stats[j.Name] = &Stats{}
	return nil
}

// Start launches every job. The jobs stop when ctx is cancelled or Stop is called.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	for _, j := range s.jobs {
		s.wg.Add(1)
		go s.loop(ctx, j)
	}
	s.logger.Info("maintenance scheduler started", "jobs", len(s.jobs))
}

// Stop cancels every job, including a run in progress, and waits for them all.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// Stats returns a copy of every job's counters.
func (s *Scheduler) Stats() map[string]Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Stats, len(s.stats))
	for k, v := range s.stats {
		out[k] = *v
	}
	return out
}

// Reschedule wakes every job that is waiting, so each recomputes its next run
// from the timing now in force (Job.Tune): a job whose interval was shortened runs
// as soon as the new interval has passed since its last run -- at once, if it
// already has -- and one whose interval was lengthened waits the longer time. A
// job in the middle of a run is not interrupted. It is what makes a change saved in
// the console take effect without a restart.
func (s *Scheduler) Reschedule() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.wake)
	s.wake = make(chan struct{})
}

func (s *Scheduler) wakeCh() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wake
}

// timing is the job's schedule in force: Tune's when it answers one, else the
// static fields, with Timeout defaulting to Every.
func (s *Scheduler) timing(j Job) Timing {
	t := Timing{Every: j.Every, Jitter: j.Jitter, Timeout: j.Timeout}
	if j.Tune != nil {
		if u := j.Tune(); u.Every > 0 {
			t = u
		}
	}
	if t.Timeout <= 0 {
		t.Timeout = t.Every
	}
	return t
}

func (s *Scheduler) jitter(t Timing) time.Duration {
	if t.Jitter <= 0 {
		return 0
	}
	return time.Duration(s.rand(int64(t.Jitter)))
}

func (s *Scheduler) loop(ctx context.Context, j Job) {
	defer s.wg.Done()
	target := s.clock.Now().Add(s.initial + s.jitter(s.timing(j)))
	// anchor is when the last run ended and jit the jitter drawn for the wait after
	// it; a Reschedule recomputes the target from both, so a wake neither restarts
	// the wait from zero nor redraws the jitter.
	var anchor time.Time
	var jit time.Duration
	ran := false
	for {
		if ctx.Err() != nil {
			s.logger.Info("maintenance job stopped", "job", j.Name)
			return
		}
		// Already due -- a Reschedule shortened the interval past the time that
		// has elapsed -- runs now rather than asking the clock for a zero wait.
		if wait := target.Sub(s.clock.Now()); wait > 0 {
			select {
			case <-ctx.Done():
				s.logger.Info("maintenance job stopped", "job", j.Name)
				return
			case <-s.wakeCh():
				if ran {
					target = anchor.Add(s.timing(j).Every + jit)
				}
				continue
			case <-s.clock.After(wait):
			}
		}
		s.runOnce(ctx, j)
		ran = true
		t := s.timing(j)
		anchor, jit = s.clock.Now(), s.jitter(t)
		target = anchor.Add(t.Every + jit)
	}
}

func (s *Scheduler) runOnce(parent context.Context, j Job) {
	timeout := s.timing(j).Timeout
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	start := s.clock.Now()
	affected, err := s.safeRun(ctx, j)
	took := s.clock.Now().Sub(start)

	interrupted := err != nil && parent.Err() != nil

	s.mu.Lock()
	st := s.stats[j.Name]
	st.Runs++
	st.LastStart, st.LastDuration, st.LastAffected = start, took, affected
	st.TotalAffected += affected
	switch {
	case interrupted:
		// Shutdown reached the pass through its context. That is Stop working, not
		// the job failing, and counting it as a failure would make every restart
		// look like an incident.
		st.Interrupted++
	case err != nil:
		st.Failures++
		st.LastError = err.Error()
	default:
		st.LastError = ""
	}
	runs, failures := st.Runs, st.Failures
	s.mu.Unlock()

	if interrupted {
		s.logger.Info("maintenance job interrupted by shutdown", "job", j.Name, "affected", affected)
		return
	}
	if err != nil {
		s.logger.Error("maintenance job failed", "job", j.Name, "affected", affected,
			"duration", took.String(), "runs", runs, "failures", failures, "error", err.Error())
		return
	}
	s.logger.Info("maintenance job ran", "job", j.Name, "affected", affected,
		"duration", took.String(), "runs", runs)
}

// safeRun turns a panic in a job into an error, so one bad pass cannot take the
// process -- and with it the Policy API every Wi-Fi sign-in asks -- down with it.
func (s *Scheduler) safeRun(ctx context.Context, j Job) (n int64, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("jobs: %s panicked: %v", j.Name, r)
		}
	}()
	return j.Run(ctx)
}
