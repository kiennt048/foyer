-- Tenant seam, database roles, and the row-level security machinery.
--
-- This is migration 0001 rather than a later one on purpose. Design spec §8.10 is
-- right that retrofitting multi-tenancy rewrites every query in the product, and a
-- tenant column added in migration 0020 is a column half the code forgets. v1 ships
-- single-tenant: exactly one row in `tenants`, and nothing in the product offers to
-- create a second. The seam is what costs nothing now and everything later.
--
-- The three roles are the other half of this file, and they are the mechanism behind
-- a rule that would otherwise be only prose: FreeRADIUS owns `radacct`
-- and `radpostauth`, and Go never writes them. Prose is enforced by whoever
-- remembers it. A GRANT is enforced by PostgreSQL.

-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
    -- The application. Full DML on Foyer's own tables; SELECT and nothing else on
    -- the two tables FreeRADIUS owns. See 00002.
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'foyer_app') THEN
        CREATE ROLE foyer_app NOLOGIN;
    END IF;

    -- The AAA data plane. Writes accounting and post-auth; reads the generated
    -- client list. Never sees an operator, a voucher batch or an audit row.
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'foyer_radius') THEN
        CREATE ROLE foyer_radius NOLOGIN;
    END IF;

    -- The single audited exception in docs/rules/radius.md: the stale-session
    -- reaper. It closes sessions a rebooted NAS never sent an Acct-Stop for, and it
    -- gets column-level UPDATE on precisely the columns that means -- see 00002.
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'foyer_reaper') THEN
        CREATE ROLE foyer_reaper NOLOGIN;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    -- The migration runner owns the schema and must be able to SET ROLE into the
    -- application role, which is how the application drops privilege without a
    -- second set of credentials to distribute and rotate. A superuser already holds
    -- every role implicitly, so this is skipped there rather than erroring.
    IF NOT pg_has_role(current_user, 'foyer_app', 'MEMBER') THEN
        EXECUTE format('GRANT foyer_app TO %I', current_user);
    END IF;
    IF NOT pg_has_role(current_user, 'foyer_radius', 'MEMBER') THEN
        EXECUTE format('GRANT foyer_radius TO %I', current_user);
    END IF;
    IF NOT pg_has_role(current_user, 'foyer_reaper', 'MEMBER') THEN
        EXECUTE format('GRANT foyer_reaper TO %I', current_user);
    END IF;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO foyer_app, foyer_radius, foyer_reaper;

CREATE TABLE tenants (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       text NOT NULL UNIQUE,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The tenant a connection is acting for, read from a session GUC.
--
-- NULL when the GUC is unset, and every policy in this schema compares with `=`, so
-- an unset GUC matches no row at all. That is deliberate: the failure mode of a
-- forgotten SET is *no data*, which is loud, rather than *all data*, which is a
-- cross-tenant leak nobody notices.
-- +goose StatementBegin
CREATE FUNCTION foyer_current_tenant() RETURNS uuid
LANGUAGE sql STABLE
AS $$
    SELECT nullif(current_setting('foyer.tenant_id', true), '')::uuid
$$;
-- +goose StatementEnd

ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenants_isolation ON tenants
    USING (id = foyer_current_tenant())
    WITH CHECK (id = foyer_current_tenant());

-- The single tenant v1 ships with. Its id is fixed rather than generated so that
-- foyerctl, the lab compose file and the tests all name the same row without having
-- to read it back first.
INSERT INTO tenants (id, slug, name)
VALUES ('00000000-0000-0000-0000-000000000001', 'default', 'Default');

GRANT SELECT ON tenants TO foyer_app;

-- +goose Down

DROP POLICY tenants_isolation ON tenants;
DROP TABLE tenants;
DROP FUNCTION foyer_current_tenant();
REVOKE USAGE ON SCHEMA public FROM foyer_app, foyer_radius, foyer_reaper;
-- The roles themselves are cluster-level and are deliberately not dropped: another
-- database in the same cluster may hold grants to them.
