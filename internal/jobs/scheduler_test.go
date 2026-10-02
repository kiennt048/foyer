package jobs_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/jobs"
)

// fakeClock fires After channels only when the test advances it, and records every
// wait it was asked for so jitter is observable.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
	asked   []time.Duration
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, waiter{at: c.now.Add(d), ch: ch})
	c.asked = append(c.asked, d)
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var keep []waiter
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
	c.mu.Unlock()
}

// waitForWaiters blocks until n goroutines are parked on After. Advancing before
// they are would fire nothing and make the test a race rather than a proof.
func (c *fakeClock) waitForWaiters(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := len(c.waiters)
		c.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d parked job(s)", n)
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func noJitter(int64) int64 { return 0 }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestEachJobIsScheduledAndRuns: a job runs once after the initial delay, then
// once per interval, and the counters say so.
func TestEachJobIsScheduledAndRuns(t *testing.T) {
	clock := newFakeClock()
	s := jobs.New(jobs.Options{Clock: clock, Logger: quiet(), Rand: noJitter, InitialDelay: time.Second})
	var a, b atomic.Int64
	for name, n := range map[string]*atomic.Int64{"a": &a, "b": &b} {
		n := n
		if err := s.Add(jobs.Job{Name: name, Every: time.Minute, Run: func(context.Context) (int64, error) {
			n.Add(1)
			return 3, nil
		}}); err != nil {
			t.Fatal(err)
		}
	}
	s.Start(context.Background())
	defer s.Stop()

	clock.waitForWaiters(t, 2)
	if a.Load() != 0 || b.Load() != 0 {
		t.Fatal("a job ran before its first wait elapsed")
	}
	clock.Advance(time.Second)
	waitFor(t, "the first pass of both jobs", func() bool { return a.Load() == 1 && b.Load() == 1 })

	clock.waitForWaiters(t, 2)
	clock.Advance(59 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if a.Load() != 1 {
		t.Fatal("a job ran again before its interval elapsed")
	}
	clock.Advance(time.Second)
	waitFor(t, "the second pass to be counted", func() bool {
		st := s.Stats()
		return st["a"].Runs == 2 && st["b"].Runs == 2
	})

	st := s.Stats()["a"]
	if st.Runs != 2 || st.TotalAffected != 6 || st.LastAffected != 3 || st.Failures != 0 {
		t.Fatalf("stats after two passes: %+v", st)
	}
}

// TestAFailingOrPanickingJobKeepsItsSchedule: an error and a panic are both counted
// and logged, and neither stops the job or the process.
func TestAFailingOrPanickingJobKeepsItsSchedule(t *testing.T) {
	clock := newFakeClock()
	s := jobs.New(jobs.Options{Clock: clock, Logger: quiet(), Rand: noJitter, InitialDelay: time.Second})
	var calls atomic.Int64
	_ = s.Add(jobs.Job{Name: "flaky", Every: time.Minute, Run: func(context.Context) (int64, error) {
		switch calls.Add(1) {
		case 1:
			return 0, errors.New("database unavailable")
		case 2:
			panic("nil map")
		}
		return 1, nil
	}})
	s.Start(context.Background())
	defer s.Stop()

	for i, step := range []time.Duration{time.Second, time.Minute, time.Minute} {
		clock.waitForWaiters(t, 1)
		clock.Advance(step)
		want := int64(i + 1)
		waitFor(t, "the next pass", func() bool { return calls.Load() == want })
	}
	waitFor(t, "the third pass to be counted", func() bool { return s.Stats()["flaky"].Runs == 3 })
	st := s.Stats()["flaky"]
	if st.Failures != 2 || st.LastError != "" || st.LastAffected != 1 {
		t.Fatalf("after an error, a panic and a success: %+v", st)
	}
}

// TestJitterIsAddedToEveryWait: the wait asked of the clock is the interval plus
// whatever the random source returned, bounded by Jitter.
func TestJitterIsAddedToEveryWait(t *testing.T) {
	clock := newFakeClock()
	var bound atomic.Int64
	s := jobs.New(jobs.Options{Clock: clock, Logger: quiet(), InitialDelay: time.Second,
		Rand: func(n int64) int64 { bound.Store(n); return n - 1 }})
	_ = s.Add(jobs.Job{Name: "j", Every: time.Minute, Jitter: 6 * time.Second,
		Run: func(context.Context) (int64, error) { return 0, nil }})
	s.Start(context.Background())
	defer s.Stop()

	clock.waitForWaiters(t, 1)
	clock.Advance(time.Second + 6*time.Second)
	clock.waitForWaiters(t, 1)
	clock.mu.Lock()
	asked := append([]time.Duration(nil), clock.asked...)
	clock.mu.Unlock()
	if len(asked) < 2 {
		t.Fatalf("asked for %d waits", len(asked))
	}
	if want := time.Second + 6*time.Second - 1; asked[0] != want {
		t.Errorf("first wait %s, want the initial delay plus jitter %s", asked[0], want)
	}
	if want := time.Minute + 6*time.Second - 1; asked[1] != want {
		t.Errorf("second wait %s, want the interval plus jitter %s", asked[1], want)
	}
	if bound.Load() != int64(6*time.Second) {
		t.Errorf("jitter drawn from [0, %d), want [0, %d)", bound.Load(), int64(6*time.Second))
	}
}

// TestStopCancelsARunningPassAndWaits: Stop reaches a pass in progress through its
// context, and returns only once every job goroutine has.
func TestStopCancelsARunningPassAndWaits(t *testing.T) {
	clock := newFakeClock()
	s := jobs.New(jobs.Options{Clock: clock, Logger: quiet(), Rand: noJitter, InitialDelay: time.Second})
	started := make(chan struct{})
	var sawCancel, returned atomic.Bool
	_ = s.Add(jobs.Job{Name: "long", Every: time.Minute, Timeout: time.Hour, Run: func(ctx context.Context) (int64, error) {
		close(started)
		<-ctx.Done()
		sawCancel.Store(errors.Is(ctx.Err(), context.Canceled))
		time.Sleep(20 * time.Millisecond) // work that finishes after cancellation
		returned.Store(true)
		return 0, ctx.Err()
	}})
	_ = s.Add(jobs.Job{Name: "idle", Every: time.Minute, Run: func(context.Context) (int64, error) { return 0, nil }})
	s.Start(context.Background())

	clock.waitForWaiters(t, 2)
	clock.Advance(time.Second)
	<-started

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
	if !sawCancel.Load() {
		t.Fatal("the running pass was not cancelled through its context")
	}
	if !returned.Load() {
		t.Fatal("Stop returned before the running pass did")
	}
	if st := s.Stats()["long"]; st.Failures != 0 || st.Interrupted != 1 {
		// Shutdown is Stop working, not the job failing (see runOnce).
		t.Fatalf("the interrupted pass: %+v", st)
	}
}

// TestAJobNeverOverlapsItself: the next wait begins when a run ends, so a pass that
// outlasts its interval is not joined by a second copy of itself.
func TestAJobNeverOverlapsItself(t *testing.T) {
	clock := newFakeClock()
	s := jobs.New(jobs.Options{Clock: clock, Logger: quiet(), Rand: noJitter, InitialDelay: time.Second})
	var inFlight, maxInFlight atomic.Int64
	release := make(chan struct{})
	var calls atomic.Int64
	_ = s.Add(jobs.Job{Name: "slow", Every: time.Second, Run: func(context.Context) (int64, error) {
		if n := inFlight.Add(1); n > maxInFlight.Load() {
			maxInFlight.Store(n)
		}
		if calls.Add(1) == 1 {
			<-release
		}
		inFlight.Add(-1)
		return 0, nil
	}})
	s.Start(context.Background())
	defer s.Stop()

	clock.waitForWaiters(t, 1)
	clock.Advance(time.Second)
	waitFor(t, "the first pass to start", func() bool { return calls.Load() == 1 })
	clock.Advance(time.Hour) // many intervals pass while it is still running
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatal("a second pass started while the first was running")
	}
	close(release)
	clock.waitForWaiters(t, 1)
	if maxInFlight.Load() != 1 {
		t.Fatalf("%d passes ran at once", maxInFlight.Load())
	}
}

// TestAddValidates: a job with no interval would spin, and a duplicate name would
// make its counters ambiguous.
func TestAddValidates(t *testing.T) {
	s := jobs.New(jobs.Options{Logger: quiet()})
	run := func(context.Context) (int64, error) { return 0, nil }
	if err := s.Add(jobs.Job{Name: "x", Run: run}); err == nil {
		t.Error("a job with no interval was accepted")
	}
	if err := s.Add(jobs.Job{Name: "x", Every: time.Minute, Run: run}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(jobs.Job{Name: "x", Every: time.Minute, Run: run}); err == nil {
		t.Error("a duplicate name was accepted")
	}
	s.Start(context.Background())
	defer s.Stop()
	if err := s.Add(jobs.Job{Name: "y", Every: time.Minute, Run: run}); !errors.Is(err, jobs.ErrStarted) {
		t.Errorf("Add after Start: %v", err)
	}
}
