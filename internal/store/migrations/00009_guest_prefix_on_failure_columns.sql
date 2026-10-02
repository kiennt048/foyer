-- The two failure columns say `guest_`, because that is what they govern.
--
-- 00007 named them `policy_failure_mode` and `policy_failure_grace_seconds`, from
-- the DDL `internal/aaa/failure.go` documented. The Go side was never ambiguous:
-- the field is `GuestFailureMode`, and `SiteFailurePolicy.For` ignores it entirely
-- for `MethodDot1X` — a method rather than a check at each call site, precisely so
-- that no call site can get it wrong.
--
-- **SQL has no `For()`.** An operator's ad-hoc query, a future admin API, a support
-- engineer reading `\d sites` at 2 a.m. — all of them see the column and none of
-- them sees the method. A wide name for a narrow fact invites the wide reading,
-- and a column comment does not travel with a `SELECT`.
--
-- There is still deliberately no column for the 802.1X answer, because it is not
-- configurable: a venue may choose to wave guests through while the control plane
-- is blind; it may not choose that for a corporate supplicant.
--
-- A rename rather than an edit to 00007, for the same reason 00007 was not an edit
-- to 00003: goose records a version as applied and never re-runs it, so an edited
-- file leaves anyone who already migrated with a schema that does not match the
-- source.

-- +goose Up

ALTER TABLE sites RENAME COLUMN policy_failure_mode          TO guest_policy_failure_mode;
ALTER TABLE sites RENAME COLUMN policy_failure_grace_seconds TO guest_policy_failure_grace_seconds;

-- The CHECK definitions follow the columns automatically; their *names* do not, and
-- the name is what `\d sites` prints. Guarded by lookup rather than asserted,
-- because PostgreSQL's auto-generated constraint name is a convention rather than a
-- promise — a database restored from a dump made by another tool may spell it
-- differently, and a migration that dies on a cosmetic rename is worse than one
-- that skips it.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'sites_policy_failure_mode_check') THEN
        ALTER TABLE sites RENAME CONSTRAINT sites_policy_failure_mode_check
                                         TO sites_guest_policy_failure_mode_check;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'sites_policy_failure_grace_seconds_check') THEN
        ALTER TABLE sites RENAME CONSTRAINT sites_policy_failure_grace_seconds_check
                                         TO sites_guest_policy_failure_grace_seconds_check;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'sites_guest_policy_failure_mode_check') THEN
        ALTER TABLE sites RENAME CONSTRAINT sites_guest_policy_failure_mode_check
                                         TO sites_policy_failure_mode_check;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'sites_guest_policy_failure_grace_seconds_check') THEN
        ALTER TABLE sites RENAME CONSTRAINT sites_guest_policy_failure_grace_seconds_check
                                         TO sites_policy_failure_grace_seconds_check;
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE sites RENAME COLUMN guest_policy_failure_grace_seconds TO policy_failure_grace_seconds;
ALTER TABLE sites RENAME COLUMN guest_policy_failure_mode          TO policy_failure_mode;
