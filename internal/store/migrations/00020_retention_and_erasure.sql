-- Retention periods, the sweep's own record, and the role that deletes.
--
-- This schema stores MAC addresses, access points, SSIDs, addresses, the page a
-- guest was going to, session history and usernames, and until this migration
-- nothing ever removed any of it. Vietnam's Personal Data Protection Law
-- (91/2025/QH15, Article 3) and the GDPR (Article 5(1)(e)) both say the same thing:
-- keep personal data only as long as its purpose needs. This migration is the
-- database half of doing that; `internal/store/retention.go` and
-- `internal/store/privacy.go` are the code half, and
-- `docs/operator/personal-data.md` is what an operator reads.
--
-- ── the exception, extended on purpose ───────────────────────────────────────
--
-- `radacct` and `radpostauth` belong to FreeRADIUS and Go has never been allowed to
-- write them, with one audited exception: the stale-session reaper's four-column
-- UPDATE (00002). A retention period and a data-subject erasure both have to
-- *remove* rows from those two tables, and nothing else can. **This is a
-- deliberate, narrow extension of that exception**, made explicitly rather than
-- allowed to happen quietly, and it is extended the same way the first one was
-- built:
--
--   * a role of its own, `foyer_retention`, holding **SELECT and DELETE and nothing
--     else**. It cannot insert an accounting row, cannot rewrite a byte counter,
--     cannot pseudonymise in place (that is an UPDATE), and cannot TRUNCATE;
--   * one Go file, `internal/store/retention.go`, which is the only file the
--     write-time guard lets name `DELETE FROM radacct` or `DELETE FROM radpostauth`;
--   * the rule text in docs/rules/radius.md and docs/rules/database.md says so.
--
-- The same role deletes from the two tables Foyer made append-only for the
-- application -- `audit_log` (00003) and `authorization_events` (00014). 00014's
-- own comment anticipated exactly this: *"retention is a sweep a separate role runs
-- rather than something the application does row by row."* `foyer_app` keeps SELECT
-- and INSERT on both and gains nothing.

-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'foyer_retention') THEN
        CREATE ROLE foyer_retention NOLOGIN;
    END IF;
    -- The migration runner must be able to SET ROLE into it, for the same reason
    -- 00001 gives for the other three. A superuser already holds every role.
    IF NOT pg_has_role(current_user, 'foyer_retention', 'MEMBER') THEN
        EXECUTE format('GRANT foyer_retention TO %I', current_user);
    END IF;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO foyer_retention;

-- DELETE needs SELECT for its WHERE clause. No INSERT, no UPDATE, no TRUNCATE, no
-- sequence USAGE: a role that cannot call nextval() cannot insert into a bigserial
-- table, and `readonly_test.go` asserts that the refusal names the *table* rather
-- than the sequence (docs/rules/database.md, "the permission test that passed while
-- the table was open").
GRANT SELECT, DELETE ON radacct, radpostauth             TO foyer_retention;
GRANT SELECT, DELETE ON audit_log, authorization_events  TO foyer_retention;

-- Read-only, and only the columns that say which venue a FreeRADIUS row belongs to.
-- A guest account's username is unique per venue (00005), so erasing `room101` at one
-- venue must leave `room101` at another alone, and the only way to tell the rows apart
-- is the console's own attribution: a NAS address to its RADIUS client, a
-- Called-Station-Id to its access point (retention.go, "Scoped to the account's
-- venue"). `tenant_id` is here because both tables' row-level policies read it.
GRANT SELECT (tenant_id, site_id, address) ON radius_clients TO foyer_retention;
GRANT SELECT (tenant_id, site_id, mac)     ON access_points  TO foyer_retention;

-- audit_log's two policies are FOR SELECT and FOR INSERT, and UPDATE and DELETE
-- deliberately have none (00003). Under row-level security a DELETE with no policy
-- affects zero rows and reports success, so without this the audit sweep would run,
-- log "removed 0", and keep every entry forever -- a sweep that looks like it works.
-- The policy is scoped TO the retention role alone: the application still has
-- neither the privilege nor a policy.
CREATE POLICY audit_log_retention ON audit_log FOR DELETE TO foyer_retention
    USING (tenant_id = foyer_current_tenant());

-- authorization_events_isolation (00014) has no FOR clause, so it already governs
-- DELETE for any role that holds the privilege; nothing to add there.

-- ---------------------------------------------------------------------------
-- retention_policies -- what an operator chose, where it differs from the default.
-- ---------------------------------------------------------------------------
--
-- **Overrides only.** The categories and their defaults live in Go
-- (`store.RetentionCategories`), because a category is code: it names the tables it
-- sweeps and the column that dates a row, and a row here naming a category the code
-- does not know would be a period nothing enforces. A category with no row takes its
-- default, so a category added by a later migration's code is enforced from its
-- first deploy without a data migration.
--
-- `days = 0` means *keep until erased or deleted by an operator*, and only the
-- categories that allow it accept it (Go validates; the CHECK is a backstop).
CREATE TABLE retention_policies (
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                    DEFAULT foyer_current_tenant(),
    category   text NOT NULL CHECK (category ~ '^[a-z][A-Za-z]{1,63}$'),
    days       integer NOT NULL CHECK (days >= 0 AND days <= 3650),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Denormalised for the audit_log's reason: the operator may be deleted later.
    updated_by text,
    PRIMARY KEY (tenant_id, category)
);

-- ---------------------------------------------------------------------------
-- retention_sweeps -- the last thing the sweep did, per category.
-- ---------------------------------------------------------------------------
--
-- A retention period nobody can see being enforced is a promise, and an operator
-- writing the retention policy Decree 356/2025/ND-CP asks for needs to be able to
-- say *when* it last ran and *what* it removed. The console reads this; the log line
-- the scheduler writes says the same thing to whoever reads logs.
CREATE TABLE retention_sweeps (
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                      DEFAULT foyer_current_tenant(),
    category     text NOT NULL,
    last_run_at  timestamptz NOT NULL,
    last_cutoff  timestamptz,
    last_removed bigint NOT NULL DEFAULT 0 CHECK (last_removed >= 0),
    -- A category, never the error text: an error from PostgreSQL can quote a value
    -- from the row it failed on, and this table is read in the console.
    last_failed  boolean NOT NULL DEFAULT false,
    PRIMARY KEY (tenant_id, category)
);

ALTER TABLE retention_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_sweeps   ENABLE ROW LEVEL SECURITY;
-- Forced, as 00019 forces every table with a policy, so the owner is not exempt
-- (`TestEveryRowLevelPolicyIsForced`).
ALTER TABLE retention_policies FORCE ROW LEVEL SECURITY;
ALTER TABLE retention_sweeps   FORCE ROW LEVEL SECURITY;

CREATE POLICY retention_policies_isolation ON retention_policies
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY retention_sweeps_isolation ON retention_sweeps
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE ON retention_policies, retention_sweeps TO foyer_app;

-- +goose Down

DROP TABLE retention_sweeps;
DROP TABLE retention_policies;
DROP POLICY audit_log_retention ON audit_log;
REVOKE ALL ON radacct, radpostauth, audit_log, authorization_events FROM foyer_retention;
REVOKE ALL ON radius_clients, access_points FROM foyer_retention;
REVOKE USAGE ON SCHEMA public FROM foyer_retention;
-- The role itself is cluster-level and is deliberately not dropped, as in 00001.
