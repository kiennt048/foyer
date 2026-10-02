package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kiennt048/foyer/internal/portal"
	"github.com/kiennt048/foyer/internal/session"
	"github.com/kiennt048/foyer/internal/store"
)

// The control plane's maintenance, as a list. `cmd/foyer` registers exactly this.
//
// Names are stable: they are what an operator greps the log for.
const (
	JobPortalSessions   = "portal-sessions"
	JobOperatorSessions = "operator-sessions"
	JobVoucherExpiry    = "voucher-expiry"
	JobStaleSessions    = "stale-sessions"
	JobRetention        = "retention"
	JobPhoneCodes       = "phone-codes"
)

// DefaultInterval is how often each job runs until an operator sets otherwise in
// the console (`updateMaintenanceSettings`, stored by `store.MaintenanceRepo`). Five
// minutes is the reaper's own historical default (`Reaper.Run`) and the floor of
// Acct-Interim-Interval (radius.md): nothing it cleans changes meaning faster than
// that. It is `store.MaintenanceLimits`' default, stated once there.
var DefaultInterval = store.DefaultMaintenance().Interval()

// Deps is what the standard jobs run against.
//
// Reaper and Retention may be nil when their pools could not be opened; the reason
// goes in ReaperErr and RetentionErr, and the job is still registered and fails
// every cycle with that reason. A maintenance task that is silently absent and one
// that ran and found nothing produce the same log -- none -- and that is the shape
// this package exists to end.
type Deps struct {
	Store        *store.Store
	Reaper       *session.Reaper
	ReaperErr    error
	Retention    *store.Retention
	RetentionErr error
	Logger       *slog.Logger
	// Settings is the operator's configuration, read every cycle. Nil is the
	// defaults.
	Settings *Settings
	// Every, when positive, fixes the interval and ignores Settings' interval. It
	// exists for tests that cannot wait a minute, the lowest interval the console
	// accepts; nothing in cmd/foyer sets it.
	Every time.Duration
	// Now is the clock the jobs pass to the repositories. Default time.Now.
	Now func() time.Time
}

// ErrUnavailable wraps the reason a job's dependency is missing.
var ErrUnavailable = errors.New("jobs: dependency unavailable")

// Standard returns the six maintenance jobs.
//
// Each one's timing is read from d.Settings before every wait (Job.Tune), and each
// run refreshes d.Settings first, so an interval, a reaper threshold or a batch size
// changed in the console is in force from the next cycle -- at once where cmd/foyer
// also wakes the scheduler -- with no restart.
func Standard(d Deps) []Job {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	interval := func() time.Duration {
		if d.Every > 0 {
			return d.Every
		}
		return d.Settings.Current().Interval()
	}
	// A pass may take longer than a short interval (a test's, or an operator's who
	// set one): a timeout equal to the interval would cancel a healthy first sweep.
	tune := func() Timing { return timingFor(interval(), 1) }
	tuneRetention := func() Timing { return timingFor(interval(), 4) }
	every := interval()
	first := tune()
	jitter, timeout := first.Jitter, first.Timeout
	// refreshed runs the job with the settings re-read first. A read that fails
	// keeps the last values (Settings.Refresh) and never fails the job.
	refreshed := func(run func(ctx context.Context) (int64, error)) func(ctx context.Context) (int64, error) {
		return func(ctx context.Context) (int64, error) {
			d.Settings.Refresh(ctx)
			return run(ctx)
		}
	}

	unavailable := func(what string, why error) error {
		if why == nil {
			why = errors.New("not configured")
		}
		return fmt.Errorf("%w: %s: %v", ErrUnavailable, what, why)
	}

	return []Job{
		{
			// The pre-authentication binding. Anyone in radio range can create one without
			// signing in (`docs/rules/security.md` §5), and nothing else bounds the table.
			Name: JobPortalSessions, Every: every, Jitter: jitter, Timeout: timeout, Tune: tune,
			Run: refreshed(func(ctx context.Context) (int64, error) {
				n, err := d.Store.Grants().ReapSessions(ctx, now())
				if err != nil {
					return n, err
				}
				// The RFC 8908 address book (00045): an entry means nothing a
				// window after it was last seen, and it names a device.
				m, err := d.Store.PortalAddresses().Reap(ctx, now().Add(-portal.DefaultAddressWindow))
				return n + m, err
			}),
		},
		{
			Name: JobOperatorSessions, Every: every, Jitter: jitter, Timeout: timeout, Tune: tune,
			Run: refreshed(func(ctx context.Context) (int64, error) {
				return d.Store.Authn().DeleteExpired(ctx)
			}),
		},
		{
			// State only: everything that authorises already checks the timestamp.
			// This is what keeps the console's voucher histogram true.
			Name: JobVoucherExpiry, Every: every, Jitter: jitter, Timeout: timeout, Tune: tune,
			Run: refreshed(func(ctx context.Context) (int64, error) {
				return d.Store.Vouchers().ExpirePast(ctx, now())
			}),
		},
		{
			// Closes sessions a rebooted NAS never sent an Acct-Stop for. A pass the
			// reaper refuses as too large to tell from an outage (MaxPerPass) is a
			// failure here, logged every cycle until somebody looks -- which is what
			// the refusal is for.
			//
			// Its two thresholds are the operator's (`staleAfterMissedUpdates`,
			// `staleCeilingPerPass`), applied before each pass.
			Name: JobStaleSessions, Every: every, Jitter: jitter, Timeout: timeout, Tune: tune,
			Run: refreshed(func(ctx context.Context) (int64, error) {
				if d.Reaper == nil {
					return 0, unavailable("the stale-session reaper (foyer_reaper)", d.ReaperErr)
				}
				v := d.Settings.Current()
				d.Reaper.Configure(session.ReaperConfig{
					SilenceMultiplier: v.StaleAfterMissedUpdates,
					MaxPerPass:        v.StaleCeilingPerPass,
				})
				closed, err := d.Reaper.Reap(ctx)
				return int64(len(closed)), err
			}),
		},
		{
			// Retention periods (store.RetentionCategories). Longer timeout than the
			// others: a first sweep over a year of accounting is large.
			Name: JobRetention, Every: every, Jitter: jitter, Timeout: 4 * timeout, Tune: tuneRetention,
			Run: refreshed(func(ctx context.Context) (int64, error) {
				if d.Retention == nil {
					return 0, unavailable("the retention connection (foyer_retention)", d.RetentionErr)
				}
				return store.SweepRetention(ctx, store.SweepTargets{App: d.Store, Retention: d.Retention,
					BatchSize: d.Settings.Current().RetentionBatchSize}, now(), logger)
			}),
		},
		{
			// Phone sign-in codes: personal data -- the number and the device -- kept only
			// as long as the per-number limit reads them. The daily ceiling's counter is a
			// separate table and is not touched.
			Name: JobPhoneCodes, Every: every, Jitter: jitter, Timeout: timeout, Tune: tune,
			Run: refreshed(func(ctx context.Context) (int64, error) {
				return d.Store.Phone().ReapCodes(ctx, now().Add(-store.PhoneCodeRetention))
			}),
		},
	}
}
