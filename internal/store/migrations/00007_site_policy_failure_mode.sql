-- The per-site failure model gets the two columns it has been reading defaults for.
--
-- `docs/rules/radius.md` and `docs/rules/security.md` §2 both require the same
-- thing: **fail closed for 802.1X, configurable fail-open for guest, per site.**
-- `internal/aaa` built the model; until now there was nowhere to store the
-- operator's answer, so every site resolved to `aaa.DefaultPolicy` and
-- `aaa.FailurePolicies` reported `configured = false`.
--
-- A new migration rather than an edit to 00003, because goose records a version as
-- applied and would never re-run an edited file. Anyone who has already migrated
-- would keep a schema that does not match the source, which is the failure mode
-- migrations exist to prevent.
--
-- ── the two decisions in this file, and why they are not stylistic ────────────
--
-- **NOT NULL DEFAULT, not nullable.** A site that has never been configured must
-- not have a NULL failure mode: every reader then has to decide what NULL means,
-- and one of them will decide it means "unset, so allow". The default is spelled
-- out here as well as in `aaa.DefaultPolicy` so that a row inserted by something
-- that never heard of that function is still fail-closed.
--
-- **These govern the guest path only.** Whatever this column says, 802.1X is
-- fail-closed -- `aaa.SiteFailurePolicy.For` ignores the mode entirely for
-- `MethodDot1X`, and it is a method rather than a check at each call site precisely
-- so that no call site can get it wrong. There is deliberately no column for the
-- 802.1X answer, because it is not configurable: a corporate supplicant is not
-- something a venue may choose to wave through while the control plane is blind.
-- The column name says `policy_` rather than `guest_policy_` to match the DDL
-- `internal/aaa/failure.go` documents; the Go field it loads into is
-- `GuestFailureMode`, and that is the name to trust.

-- +goose Up

ALTER TABLE sites
    ADD COLUMN policy_failure_mode text NOT NULL DEFAULT 'fail-closed'
        CHECK (policy_failure_mode IN ('fail-closed', 'fail-open')),
    -- The floor is 60 rather than 0: a grace period shorter than one
    -- re-authentication round trip is a fail-open setting that fails closed in
    -- practice, which is the worst of both -- an operator who chose to let guests
    -- on gets an outage anyway and no signal that the number is why.
    --
    -- The ceiling is `aaa.MaxGraceSeconds`. A grace period longer than the outages
    -- it covers stops being a grace period: it is a site that authorizes nobody and
    -- lets everybody on. The default of 300 matches the widely-implemented
    -- Acct-Interim-Interval floor, so the first accounting sample of a degraded
    -- session is also the moment it has to ask again.
    ADD COLUMN policy_failure_grace_seconds integer NOT NULL DEFAULT 300
        CHECK (policy_failure_grace_seconds BETWEEN 60 AND 3600);

-- +goose Down

ALTER TABLE sites
    DROP COLUMN policy_failure_grace_seconds,
    DROP COLUMN policy_failure_mode;
