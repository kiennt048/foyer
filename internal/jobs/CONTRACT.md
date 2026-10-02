# internal/jobs — CONTRACT

Foyer's maintenance scheduler, and the list of jobs it runs. It runs in the
`maintenance` service (`foyer maintenance`) when deployed from `deploy/`, and inside
the control plane's own process otherwise.
Every cleanup function the store offers has a caller here, so none of them exists only
in its tests.

## Invariants

1. **`Standard` is the whole maintenance list, and `cmd/foyer` registers exactly it.**
   `portal-sessions` (`GrantRepo.ReapSessions`), `operator-sessions`
   (`OperatorSessionRepo.DeleteExpired`), `voucher-expiry` (`VoucherRepo.ExpirePast`),
   `stale-sessions` (`session.Reaper.Reap`), `retention` (`store.SweepRetention`) and
   `phone-codes` (`PhoneRepo.ReapCodes`).
   `TestTheStandardJobsAreTheSixNamed` pins the list and
   `cmd/foyer`'s `TestTheControlPlaneSchedulesTheStandardJobs` pins the wiring.

2. **A missing dependency is a failure every cycle, never an absent job.** When the
   reaper's or the retention role's pool cannot open, the job is still registered and
   returns `ErrUnavailable` with the reason each time it runs, logged at ERROR. A job
   that is silently not there and one that ran and found nothing produce the same log.

3. **A job never overlaps itself, and keeps its schedule through errors and panics.**
   The next wait begins when a run ends. A panic is recovered into an error: this
   process also serves the Policy API, and one bad sweep must not take every Wi-Fi
   sign-in down with it.

4. **`Stop` cancels a pass in progress through its context and waits for every
   goroutine.** An interrupted pass is counted as `Interrupted`, not as a failure.

5. **Every run writes one structured log line** — job, rows affected, duration, run
   count — and `Stats()` holds the same counters in-process. There is no metrics
   series for them and no metrics client library in the dependencies; the retention
   sweep additionally records its outcome in `retention_sweeps`, which the console
   reads.

6. **Each run is bounded** by a timeout of at least a minute (four for retention) and
   each wait carries up to 10 % jitter, so two control planes during a blue/green
   cutover do not sweep in lockstep. Both are **derived from the interval, never
   set**, so no configuration cancels a healthy pass with its own timeout.

7. **The schedule is the operator's and needs no restart**: every operator setting
   is set in the admin console and takes effect without a restart. The interval,
   the reaper's two thresholds and the retention batch are
   `store.MaintenanceRepo`'s row, set in the console. `Settings` is re-read at the
   start of every run (at most once per 10 s across all jobs), and each job's timing
   is asked of it before every wait (`Job.Tune`). `Scheduler.Reschedule` wakes every
   waiting job to recompute its next run from its last one; `cmd/foyer` calls it
   after a write through `apistore.Config.MaintenanceChanged`. A failed or
   out-of-range read keeps the values in force. There is no environment variable.
   Proven by `TestAnIntervalChangedInTheConsoleReschedulesARunningJob`,
   `TestSettingsAreReadAgainEachCycleAndSurviveAFailedRead` and
   `TestTheStandardJobsTakeTheirTimingFromTheSettings`.

## Proven by

`scheduler_test.go` against a fake clock (scheduling, jitter, failure and panic,
non-overlap, stop); `standard_test.go` against real PostgreSQL 16 on the real clock
(expired rows for every job gone after one cycle, live rows kept).

## The maintenance container's door

8. **The jobs may run in a process of their own** — `foyer maintenance`, the
   `maintenance` service in `deploy/compose.yaml` — which alone holds the reaper's and
   the retention role's logins (PacketFence's `pfcron` shape). `remote.go` is the
   door between it and the control plane.

9. **Only a caller presenting the token gets in**, compared in constant time; a
   token under 32 bytes is refused on both sides (`ErrWeakToken`). `/healthz` is the
   one unauthenticated answer and says nothing. `TestTheMaintenanceDoorAdmitsOnlyTheToken`.

10. **An erasure crosses the door as a kind and one value, and the subject is
    rebuilt on the maintenance side** by `store.MACSubject`, `store.PhoneSubject` or
    `store.AccountSubject` — a caller cannot hand it a built `store.Subject`, and an
    extra member is ignored, never obeyed. The retention half runs on that side's
    connection; the counts come back. `TestAnErasureCrossesTheDoorAsTheSameSubject`,
    `TestTheDoorRebuildsTheSubjectAndRefusesWhatIsNotOne`, and over PostgreSQL
    `TestAnErasureThroughTheDoorRemovesTheRetainedRows`.

11. **"The settings changed" is a notification, not a value**: the maintenance
    side re-reads its own row and wakes the scheduler. A lost notification delays
    an edit to the next cycle, which re-reads the row anyway.
    `TestASettingsChangeReachesTheScheduler`.
