-- The console's first-access setup, and the end of the shipped password.
--
-- This reverses the decision behind 00017: Foyer no longer ships `admin` with a
-- known password. A fresh install has no operator; the installer opens a one-time
-- setup with a random code (`foyerctl open-setup`, run by `deploy/first-boot.sh`,
-- which prints it), and the console's `completeAdminSetup` uses that code once to
-- create the first administrator with a username and a password of the operator's
-- own. `internal/api/handlers_setup.go` has the survey (authentik, UniFi, Omada,
-- Grafana, Jenkins, GitLab, Keycloak, OWASP ASVS) and why this is the Jenkins
-- shape.
--
-- ── console_setup ────────────────────────────────────────────────────────────
--
-- One row per tenant, and **the code is never stored**, only its SHA-256
-- (`identity.SetupCodeDigest`): an 80-bit random value needs no slow hash, and a
-- database read must not hand out the right to create an administrator.
-- `code_digest` is NULL exactly when the setup is complete, so "open" is one
-- column and the CHECK keeps the two from disagreeing. `completeAdminSetup` locks
-- the row, compares, inserts the operator and closes the row in one transaction,
-- which is what makes two racing browsers produce one administrator.
--
-- ── the rows this migration deletes ─────────────────────────────────────────
--
-- An operator named `admin` that still has `must_change_password` set holds a
-- temporary password an earlier installer set, not one of the operator's own. It
-- could do nothing but change that password, so deleting the row loses no work and
-- removes a known credential from an upgraded install. The upgrade then finds no
-- administrator, `upgrade.sh` opens the setup like a fresh install, and the
-- installer's output carries the code. Audit entries name their actor by value, so
-- the log keeps them.

-- +goose Up

CREATE TABLE console_setup (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    code_digest  bytea CHECK (code_digest IS NULL OR octet_length(code_digest) = 32),
    opened_at    timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    -- Denormalised like every actor column: it outlives the operator it names.
    completed_by text,
    CHECK ((code_digest IS NULL) = (completed_at IS NOT NULL))
);

ALTER TABLE console_setup ENABLE ROW LEVEL SECURITY;
-- Forced, as 00019 forces every table with a policy (`TestEveryRowLevelPolicyIsForced`).
ALTER TABLE console_setup FORCE ROW LEVEL SECURITY;

CREATE POLICY console_setup_isolation ON console_setup
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE ON console_setup TO foyer_app;

COMMENT ON TABLE console_setup IS
    'The first-access setup. code_digest is SHA-256 of the installer''s setup code, '
    'NULL once completeAdminSetup has succeeded. Opened only by foyerctl open-setup.';

-- Across every tenant, so the owner steps outside the forced policy for this one
-- statement: with FORCE on and no `foyer.tenant_id` set, the DELETE would match
-- nothing and report success, silently.
--
-- Only that operator: one an administrator created with
-- `foyerctl create-operator --must-change-password` holds a temporary password of
-- their own choosing and is kept.
-- `TestFirstAccessMigrationDeletesOnlyTheShippedAdmin` holds it.
ALTER TABLE operators NO FORCE ROW LEVEL SECURITY;
DELETE FROM operators WHERE must_change_password AND username = 'admin';
ALTER TABLE operators FORCE ROW LEVEL SECURITY;

COMMENT ON COLUMN operators.must_change_password IS
    'This operator is on a temporary password somebody else set. While true the API '
    'refuses every operation but getAuthSession, signOut and changeOwnPassword. Set '
    'only by foyerctl create-operator --must-change-password; cleared only by a '
    'successful password change.';

-- +goose Down

COMMENT ON COLUMN operators.must_change_password IS
    'This operator holds the credential the install shipped with. While true the API '
    'refuses every operation but getAuthSession, signOut and changeOwnPassword. Set '
    'only by foyerctl create-operator --must-change-password; cleared only by a '
    'successful password change.';

-- The deleted operators are not restored: their password was the published one.
DROP TABLE console_setup;
