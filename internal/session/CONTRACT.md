# internal/session — CONTRACT

## Invariants

1. **FreeRADIUS owns `radacct`.** Go never inserts or alters an accounting row, with
   exactly one audited exception below, and never deletes one outside
   `internal/store/retention.go` — the deliberate retention/erasure exception,
   which is `DELETE` only and never touches an open session. The stock column set is kept
   verbatim: it is the project's exit hatch, since the data stays readable by a stock
   FreeRADIUS installation if Foyer disappears.

2. **The audited exception is the stale-session reaper, and it is bounded three
   ways.** A session whose NAS rebooted never receives an `Acct-Stop` and stays open
   forever if nothing closes it — the classic RADIUS accounting failure, which
   `docs/rules/radius.md` says not to rediscover.

   - **Its whole write surface is `reaper.sql`**: one statement, embedded, composing
     nothing at runtime. An exception nobody can read in one sitting is not audited,
     it is asserted.
   - **PostgreSQL enforces the boundary independently.** The reaper connects as
     `foyer_reaper`, which holds `SELECT` plus column-level `UPDATE` on exactly
     `acctstoptime`, `acctterminatecause`, `acctsessiontime`, `acctupdatetime`, and
     has no privilege at all on `radpostauth`. A fifth column is a schema change
     (a new migration and a review of this exception), not an edit here.
   - **Every closed row is logged individually.** A reaper whose log says
     "closed 37" and a bug that closed 37 live sessions are the same log line. The
     line names the device by its masked MAC (`internal/redact`), never in full: a
     log is a copy of personal data no erasure reaches.

3. **The reaper believes the timestamps, not the clock.** `acctstoptime` is the last
   moment anything was heard from the session, never `now()`: a NAS that rebooted on
   Friday would otherwise bill the whole weekend as connected time, on a row a quota
   or an invoice reads. `acctsessiontime` follows from the same two timestamps.
   `acctupdatetime` is the one column that means *we touched this*, which is how a
   reader tells a reaped session from one a NAS closed properly.

4. **Two thresholds, deliberately different numbers.** The console *marks* a session
   orphaned at twice its reporting interval; the reaper *closes* it at four times. A
   session closed the instant it becomes orphaned is a session no operator ever sees
   as orphaned — and "sessions keep going orphaned on that access point" is the
   signal that a NAS is misbehaving. The interval floor is 300 s, because
   `Acct-Interim-Interval` has a widely-implemented five-minute floor and a NAS
   claiming ten seconds is not evidence that forty seconds of silence means anything.

5. **A pass large enough to be an outage is refused, and nothing is written.** A
   site-wide outage and a bug in the silence arithmetic produce the same query result
   and the reaper cannot tell them apart. Those sessions are already over, so closing
   them an hour later with somebody watching costs nothing; closing four hundred live
   ones costs a venue.

6. **32-bit counter wrap is reconstructed, never subtracted around.**
   `Acct-Input-Octets` and `Acct-Output-Octets` are 32-bit and wrap at 4 GiB;
   `Acct-Input-Gigawords` / `Acct-Output-Gigawords` carry the high word (RFC 2866
   §5.3–5.4, RFC 2869 §5.1–5.2). Subtracting two raw counters across a wrap makes
   "used" negative and hands the guest 4 GiB of free quota. `wrap.go` reconstructs the
   64-bit value; the reaper touches no counter at all and could not if it tried.

7. **Freshness travels with every accounting-derived figure.** Quota is sampled,
   never real-time. Missing accounting means *session ended*, never *unlimited*.

## Proven by

`internal/session/reaper_test.go`, against real PostgreSQL as `foyer_reaper`.
The grant boundary is shown to fail when it should: a fifth column in `reaper.sql`'s `SET` list turns three of the six red with
`permission denied for table radacct` — naming the table, not a sequence, which is
what `docs/rules/database.md` requires of a negative test here.

`internal/store/readonly_test.go` proves the grant is narrow from the other side.
Neither half is sufficient: a grant nothing exercises is a grant that is right by
luck.
