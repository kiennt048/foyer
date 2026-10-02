package jobs_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/jobs"
	"github.com/kiennt048/foyer/internal/store"
)

// TestAnIntervalChangedInTheConsoleReschedulesARunningJob is the
// admin-configurability rule at the scheduler: a job already waiting a long
// interval runs on the new, shorter one after Reschedule, without a restart. An
// interval read once at start would leave a job waiting out whatever it had been
// started with.
func TestAnIntervalChangedInTheConsoleReschedulesARunningJob(t *testing.T) {
	clock := newFakeClock()
	s := jobs.New(jobs.Options{Clock: clock, Logger: quiet(), Rand: noJitter, InitialDelay: time.Second})
	settings := jobs.NewSettings(nil, quiet())
	long := store.DefaultMaintenance()
	long.IntervalSeconds = 3600
	settings.Apply(long)
	var runs atomic.Int64
	if err := s.Add(jobs.Job{Name: "j", Every: time.Minute,
		Tune: func() jobs.Timing { return jobs.Timing{Every: settings.Current().Interval()} },
		Run:  func(context.Context) (int64, error) { runs.Add(1); return 0, nil }}); err != nil {
		t.Fatal(err)
	}
	s.Start(context.Background())
	defer s.Stop()

	clock.waitForWaiters(t, 1)
	clock.Advance(time.Second)
	waitFor(t, "the first pass", func() bool { return runs.Load() == 1 })
	clock.waitForWaiters(t, 1)

	// Ten minutes into an hour's wait, the operator saves five minutes.
	clock.Advance(10 * time.Minute)
	time.Sleep(20 * time.Millisecond)
	if runs.Load() != 1 {
		t.Fatal("the job ran before either interval elapsed")
	}
	short := long
	short.IntervalSeconds = 300
	settings.Apply(short)
	s.Reschedule()
	// Five minutes have already passed since the last run ended, so it is due now.
	waitFor(t, "the pass the shorter interval made due", func() bool { return runs.Load() == 2 })

	// And the wait after it is the new interval, not the old one.
	clock.waitForWaiters(t, 1)
	clock.Advance(5 * time.Minute)
	waitFor(t, "a pass one new interval later", func() bool { return runs.Load() == 3 })
}

// TestSettingsAreReadAgainEachCycleAndSurviveAFailedRead: a second control plane
// that did not serve the write sees it on its next read; a read that fails keeps
// the schedule it had.
func TestSettingsAreReadAgainEachCycleAndSurviveAFailedRead(t *testing.T) {
	var reads atomic.Int64
	var fail atomic.Bool
	stored := store.DefaultMaintenance()
	stored.StaleCeilingPerPass = 17
	settings := jobs.NewSettings(func(context.Context) (store.MaintenanceValues, error) {
		reads.Add(1)
		if fail.Load() {
			return store.MaintenanceValues{}, errors.New("connection refused")
		}
		return stored, nil
	}, quiet())
	if got := settings.Current(); got != store.DefaultMaintenance() {
		t.Fatalf("before any read: %+v, want the defaults", got)
	}
	settings.Refresh(context.Background())
	if settings.Current().StaleCeilingPerPass != 17 {
		t.Fatalf("after a read: %+v", settings.Current())
	}
	settings.Refresh(context.Background()) // within the TTL: no second query
	if reads.Load() != 1 {
		t.Fatalf("%d reads inside one TTL, want 1", reads.Load())
	}

	// A read that fails, and a row out of range (which the CHECKs forbid), are
	// neither of them a schedule to follow: the values in force stay.
	fail.Store(true)
	failing := jobs.NewSettings(func(ctx context.Context) (store.MaintenanceValues, error) {
		return store.MaintenanceValues{}, errors.New("connection refused")
	}, quiet())
	failing.Refresh(context.Background())
	bogus := jobs.NewSettings(func(ctx context.Context) (store.MaintenanceValues, error) {
		v := store.DefaultMaintenance()
		v.IntervalSeconds = 1
		return v, nil
	}, quiet())
	bogus.Refresh(context.Background())
	for _, st := range []*jobs.Settings{failing, bogus} {
		for _, j := range jobs.Standard(jobs.Deps{Settings: st}) {
			if j.Tune().Every != jobs.DefaultInterval {
				t.Fatalf("%s: %s, want the default kept", j.Name, j.Tune().Every)
			}
		}
	}
}

// TestTheStandardJobsTakeTheirTimingFromTheSettings: every standard job's
// interval is the operator's, and the retention sweep's timeout is four of the
// others'.
func TestTheStandardJobsTakeTheirTimingFromTheSettings(t *testing.T) {
	settings := jobs.NewSettings(nil, quiet())
	v := store.DefaultMaintenance()
	v.IntervalSeconds = 1800
	settings.Apply(v)
	for _, j := range jobs.Standard(jobs.Deps{Settings: settings}) {
		tm := j.Tune()
		if tm.Every != 30*time.Minute || tm.Jitter != 3*time.Minute {
			t.Errorf("%s: %+v, want every 30m with 3m jitter", j.Name, tm)
		}
		want := 30 * time.Minute
		if j.Name == jobs.JobRetention {
			want *= 4
		}
		if tm.Timeout != want {
			t.Errorf("%s: timeout %s, want %s", j.Name, tm.Timeout, want)
		}
	}
}
