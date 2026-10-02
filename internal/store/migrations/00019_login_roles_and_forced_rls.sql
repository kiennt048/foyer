-- The application and FreeRADIUS log in as their own roles, and nobody but the
-- migration runner is the schema owner.
--
-- ── what was true until this migration ───────────────────────────────────────
--
-- 00001 created `foyer_app`, `foyer_radius` and `foyer_reaper` NOLOGIN, and every
-- process then authenticated as the schema owner. In `deploy/compose.yaml` that is
-- `POSTGRES_USER: foyer`, which the official image makes a **superuser**:
--
--   * the control plane logged in as that superuser and ran `SET ROLE foyer_app`
--     after connecting. `SET ROLE` is not a privilege boundary — `RESET ROLE` is a
--     statement any session may issue, and it returns to the session user. One SQL
--     injection anywhere in the product was therefore a superuser, which in
--     PostgreSQL is `COPY … TO PROGRAM`, i.e. a shell on the database host.
--   * FreeRADIUS logged in as the same superuser and did not drop anything at all.
--     The data plane, the process that parses every packet any device in radio
--     range can send, could read `operators`, `shared_secrets` and the NT hashes.
--
-- The roles with the right privileges existed from the first migration; nothing
-- logged in as them.
--
-- ── what the field does ──────────────────────────────────────────────────────
--
-- FreeRADIUS's own `mods-config/sql/main/postgresql/setup.sql` (3.2.x) creates a
-- dedicated `radius` login with SELECT on the policy tables and `nas`, and
-- SELECT/INSERT/UPDATE on `radacct` and `radpostauth` — exactly `foyer_radius`'s
-- grant set in 00002. PostgREST's `authenticator` role does use `SET ROLE`, and
-- its documentation is explicit about why that is safe *there*: the login role
-- holds no privilege of its own, so returning to it gains nothing. That is the
-- property this migration makes true for Foyer, by the simpler route: the login
-- role IS the privilege role. `foyer_app` logs in as `foyer_app`; `RESET ROLE`
-- lands on `foyer_app`.
--
-- ── what this migration does, and what it deliberately does not ──────────────
--
-- It grants LOGIN. It sets **no password**, because a password in a migration is a
-- password in the repository, which `docs/rules/security.md` §7 forbids. A
-- LOGIN role whose password is NULL cannot authenticate by password at all, so
-- the state this leaves is "can log in once somebody chooses a secret", never
-- "can log in with a known one". `foyerctl migrate` sets the passwords from
-- FOYER_APP_DB_PASSWORD / FOYER_RADIUS_DB_PASSWORD, which deploy/first-boot.sh
-- generates on the machine — see `store.SetLoginPassword`, which sends a SCRAM
-- verifier rather than the password, so the secret never reaches a server log.
--
-- `foyer_reaper` stays NOLOGIN. Nothing runs it yet; whoever wires the reaper
-- gives it a login the same way, rather than inheriting one it does not use.
--
-- +goose Up

ALTER ROLE foyer_app LOGIN;
ALTER ROLE foyer_radius LOGIN;

-- ── FORCE ROW LEVEL SECURITY, on every table that has a policy ────────────────
--
-- Without FORCE a table's OWNER bypasses its policies. That was written down as a
-- feature in store/CONTRACT.md §4 ("migrations, backup and restore are the
-- owner"), and it is one only while the owner is the migration runner and nothing
-- else. FORCE removes the owner's exemption so that the day anything other than
-- the migrator owns a table, the policy still applies to it.
--
-- **What FORCE does not do, stated so nobody mistakes it for more:** superusers and
-- BYPASSRLS roles skip row security with or without it (PostgreSQL docs, "Row
-- Security Policies"). The migrator is still a superuser in deploy/, so migrations,
-- `pg_dump` and restore behave exactly as before. It is defence in depth for a
-- non-superuser owner, not a restriction on the one that exists.
--
-- Listed rather than looped, because an applied migration is a record of what it
-- did. A table added later with ENABLE and no FORCE is caught by
-- `TestEveryRowLevelPolicyIsForced`, not by this file.
ALTER TABLE tenants                 FORCE ROW LEVEL SECURITY;
ALTER TABLE sites                   FORCE ROW LEVEL SECURITY;
ALTER TABLE operators               FORCE ROW LEVEL SECURITY;
ALTER TABLE operator_sessions       FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_log               FORCE ROW LEVEL SECURITY;
ALTER TABLE controllers             FORCE ROW LEVEL SECURITY;
ALTER TABLE controller_vendor_sites FORCE ROW LEVEL SECURITY;
ALTER TABLE shared_secrets          FORCE ROW LEVEL SECURITY;
ALTER TABLE radius_clients          FORCE ROW LEVEL SECURITY;
ALTER TABLE access_points           FORCE ROW LEVEL SECURITY;
ALTER TABLE voucher_batches         FORCE ROW LEVEL SECURITY;
ALTER TABLE vouchers                FORCE ROW LEVEL SECURITY;
ALTER TABLE accounts                FORCE ROW LEVEL SECURITY;
ALTER TABLE account_nt_hashes       FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_settings         FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_hero_images      FORCE ROW LEVEL SECURITY;
ALTER TABLE walled_garden_entries   FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_grants           FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_sessions         FORCE ROW LEVEL SECURITY;
ALTER TABLE authorization_events    FORCE ROW LEVEL SECURITY;
ALTER TABLE account_pap_verifiers   FORCE ROW LEVEL SECURITY;

-- ── the credential view gets an owner that is minimally privileged too ────────
--
-- 00016's `radius_pap_verifiers` is `security_invoker = false`, so its query runs
-- with the privileges — and under the row security — of the view's OWNER. 00016
-- says the owner "bypasses RLS (no table here uses FORCE ROW LEVEL SECURITY)".
-- **That sentence stops being true in this file**, and the trap it described is
-- the one FORCE springs: had the owner been an ordinary table owner, FORCE would
-- have applied `accounts_isolation` to it, FreeRADIUS never sets the tenant GUC,
-- the view would return nothing, and every 802.1X password would be "wrong".
--
-- It survives here only because the owner is a superuser — which is the other
-- half of the problem: the "minimally-privileged view" `internal/aaa/CONTRACT.md`
-- promises was executing with superuser privileges. So the view is handed to a
-- role that holds exactly what the view reads and nothing else:
--
--   * column-level SELECT on the five columns the view's query names, so
--     `opted_in_by`, the argon2id hash and every other column stay unreadable
--     even to the view's owner;
--   * a SELECT-only policy for that role alone, USING (true). Cross-tenant on
--     purpose: FreeRADIUS's identity namespace is flat and
--     `account_pap_verifiers_username_is_global` already makes it so. This is the
--     same fact as `nas` having no policy, confined to one role and one command.
--
-- NOLOGIN, and nobody logs in as it or is granted it except the migration runner,
-- which has to be able to hand it the view.

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'foyer_credential_view') THEN
        CREATE ROLE foyer_credential_view NOLOGIN;
    END IF;
    IF NOT pg_has_role(current_user, 'foyer_credential_view', 'MEMBER') THEN
        EXECUTE format('GRANT foyer_credential_view TO %I', current_user);
    END IF;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO foyer_credential_view;
GRANT SELECT (id, enabled) ON accounts TO foyer_credential_view;
GRANT SELECT (account_id, username, verifier) ON account_pap_verifiers TO foyer_credential_view;

CREATE POLICY accounts_credential_view ON accounts
    FOR SELECT TO foyer_credential_view USING (true);
CREATE POLICY account_pap_verifiers_credential_view ON account_pap_verifiers
    FOR SELECT TO foyer_credential_view USING (true);

ALTER VIEW radius_pap_verifiers OWNER TO foyer_credential_view;

-- ── `foyerctl doctor` runs as the application now ────────────────────────────
--
-- Its migration check reads goose's version table, and until today it did so as
-- the superuser. Read-only metadata; without this the doctor reports "migrations
-- could not be read" on every healthy install, which is the check crying wolf.
GRANT SELECT ON goose_db_version TO foyer_app;

-- +goose Down

REVOKE SELECT ON goose_db_version FROM foyer_app;

ALTER VIEW radius_pap_verifiers OWNER TO CURRENT_USER;
DROP POLICY account_pap_verifiers_credential_view ON account_pap_verifiers;
DROP POLICY accounts_credential_view ON accounts;
REVOKE ALL ON accounts, account_pap_verifiers FROM foyer_credential_view;
REVOKE USAGE ON SCHEMA public FROM foyer_credential_view;

ALTER TABLE tenants                 NO FORCE ROW LEVEL SECURITY;
ALTER TABLE sites                   NO FORCE ROW LEVEL SECURITY;
ALTER TABLE operators               NO FORCE ROW LEVEL SECURITY;
ALTER TABLE operator_sessions       NO FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_log               NO FORCE ROW LEVEL SECURITY;
ALTER TABLE controllers             NO FORCE ROW LEVEL SECURITY;
ALTER TABLE controller_vendor_sites NO FORCE ROW LEVEL SECURITY;
ALTER TABLE shared_secrets          NO FORCE ROW LEVEL SECURITY;
ALTER TABLE radius_clients          NO FORCE ROW LEVEL SECURITY;
ALTER TABLE access_points           NO FORCE ROW LEVEL SECURITY;
ALTER TABLE voucher_batches         NO FORCE ROW LEVEL SECURITY;
ALTER TABLE vouchers                NO FORCE ROW LEVEL SECURITY;
ALTER TABLE accounts                NO FORCE ROW LEVEL SECURITY;
ALTER TABLE account_nt_hashes       NO FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_settings         NO FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_hero_images      NO FORCE ROW LEVEL SECURITY;
ALTER TABLE walled_garden_entries   NO FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_grants           NO FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_sessions         NO FORCE ROW LEVEL SECURITY;
ALTER TABLE authorization_events    NO FORCE ROW LEVEL SECURITY;
ALTER TABLE account_pap_verifiers   NO FORCE ROW LEVEL SECURITY;

-- LOGIN is deliberately NOT revoked, for 00001's reason: role attributes are
-- cluster-level, and a Down in one database cannot know that another database in
-- the same cluster is not logging in as these roles right now. A role with no
-- password still cannot authenticate by password.
