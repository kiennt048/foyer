-- The stale-session reaper's entire write surface.
--
-- Everything Go is ever permitted to write to `radacct` is in this file, in one
-- statement. That is the point of it being a file: `docs/rules/radius.md` calls
-- the reaper "the single audited exception", and an exception nobody can read in
-- one sitting is not audited, it is asserted. The Go beside it decides *when* to
-- run this and logs what it closed; it composes no SQL.
--
-- The database enforces the boundary independently. `foyer_reaper` holds
-- `SELECT ON radacct` and `UPDATE (acctstoptime, acctterminatecause,
-- acctsessiontime, acctupdatetime)` -- four columns, from migration 00002 -- and
-- nothing on `radpostauth` at all. A fifth column here is refused by PostgreSQL
-- before it is refused by review.
--
-- $1 terminate cause   $2 interval multiplier   $3 interval floor (seconds)
-- $4 batch limit
--
-- The three timestamps, and why none of them is now():
--
--   acctstoptime    The last moment anything was heard from the session. The
--                   session ended at or before then; now() would invent the whole
--                   silent period as connected time, and that period is unbounded
--                   -- a NAS that rebooted on Friday would bill until Monday.
--   acctsessiontime The elapsed time between the start and that same last moment.
--                   Derived from the two timestamps, never from a byte counter:
--                   `internal/session/wrap.go` exists because subtracting raw
--                   RADIUS counters is how "remaining" hands a guest 4 GiB.
--   acctupdatetime  now(). This is the one column that means "we touched it", and
--                   it is why the read model can tell a reaped session from one a
--                   NAS closed: acctupdatetime > acctstoptime happens no other way.
--
-- FOR UPDATE SKIP LOCKED, because two control planes during a blue/green cutover
-- must not fight over the same rows or block each other.

UPDATE radacct
SET acctstoptime       = coalesce(acctupdatetime, acctstarttime),
    acctterminatecause = $1::text,
    acctsessiontime    = greatest(0, floor(extract(epoch FROM
                             coalesce(acctupdatetime, acctstarttime) - acctstarttime))::bigint),
    acctupdatetime     = now()
WHERE acctuniqueid IN (
    SELECT acctuniqueid
    FROM radacct
    WHERE acctstoptime IS NULL
      AND acctstarttime IS NOT NULL
      AND coalesce(acctupdatetime, acctstarttime)
          < now() - (($2::int * greatest(coalesce(acctinterval, $3::int), $3::int))::text
                     || ' seconds')::interval
    ORDER BY coalesce(acctupdatetime, acctstarttime)
    LIMIT $4::int
    FOR UPDATE SKIP LOCKED
)
RETURNING acctuniqueid,
          acctsessionid,
          username,
          host(nasipaddress),
          coalesce(callingstationid, ''),
          acctstarttime,
          acctstoptime,
          acctsessiontime;
