-- Transport and RADIUS-plane settings, stored where the console can change them
-- (the admin-configurability rule: every operator setting is configurable in the
-- admin console, stored in the database, and takes effect without a restart).
--
-- Until this migration every value here was an environment variable, a file under
-- /opt/foyer/secrets, a constant in code or a `foyerctl` flag:
--
--   portal_url, venue_url        FOYER_PORTAL_URL, FOYER_VENUE_URL
--   network_certificates         /opt/foyer/secrets/portal-tls/{cert,key}.pem,
--     (purpose portal-tls)       read through FOYER_PORTAL_TLS_CERT/_KEY
--   throttles, body_ceilings     constants in internal/api/hardening.go
--   radius_max_servers           `foyerctl generate-raddb --max-servers`
--   eap_tls_enabled,             `foyerctl apply-raddb --eap-cert-dir`
--     network_certificates
--     (purpose eap-tls)
--   radius_client_coa_timing     fixed in internal/nas (2 s, no retries)
--   radius_operations            what `foyerctl sync-nas` printed to a terminal
--
-- The environment values stay as bootstrap fallbacks. A value stored here wins.
--
-- ── the private keys are enveloped ───────────────────────────────────────────
--
-- `private_key_sealed` is AES-256-GCM under a per-row data key wrapped by the KEK
-- from FOYER_KEK, the same envelope as a RADIUS shared secret
-- (`docs/rules/database.md`), bound to its column and purpose by the associated
-- data. The certificates themselves are public — every guest's browser is shown
-- the portal's — so they are stored as PEM text.
--
-- ── bounds are the API's ─────────────────────────────────────────────────────
--
-- The minimum and maximum of every number here are enforced, with the figure, by
-- `internal/api/netsettings.go`, which is also what reports them to the console.
-- The CHECKs below refuse only what is nonsense at any bound — a negative wait, a
-- zero-sized pool — so the two cannot disagree about a legitimate value.
--
-- ── row-level security is FORCED ─────────────────────────────────────────────
--
-- Tenant-scoped like the rest of the schema, and forced, so the migration runner's
-- own connection cannot read across tenants either.

-- +goose Up

CREATE TABLE network_settings (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    -- NULL is "not set here": the deployment's bootstrap value applies.
    portal_url text,
    venue_url  text,
    -- The contract's `SignInThrottles` and `BodyCeilings`, verbatim. NULL is the
    -- defaults in code.
    throttles     jsonb,
    body_ceilings jsonb,
    radius_max_servers integer CHECK (radius_max_servers > 0),
    eap_tls_enabled    boolean NOT NULL DEFAULT false,
    -- When a generator setting last changed, against which the last apply is
    -- compared: a setting saved after it is one the running server does not have.
    radius_changed_at timestamptz,
    updated_at timestamptz,
    updated_by text
);

CREATE TABLE network_certificates (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    purpose text NOT NULL CHECK (purpose IN ('portal-tls', 'eap-tls')),
    certificate_pem    text  NOT NULL,
    private_key_sealed bytea NOT NULL,
    -- EAP-TLS only: the CA that issued the supplicants' certificates.
    ca_pem text,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by text NOT NULL,
    PRIMARY KEY (tenant_id, purpose),
    CHECK ((purpose = 'eap-tls') = (ca_pem IS NOT NULL))
);

-- Per `clients.conf` row, because every access point behind one row is the same
-- device on the wire: the shared secret is per row for the same reason.
CREATE TABLE radius_client_coa_timing (
    radius_client_id text PRIMARY KEY REFERENCES radius_clients(id) ON DELETE CASCADE,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    timeout_ms integer NOT NULL CHECK (timeout_ms > 0),
    retries    integer NOT NULL CHECK (retries >= 0)
);

-- The last sync of the client table and the last apply of a generated tree, as the
-- contract's `RadiusClientSync` and `RadiusApply`. One row per kind: the console
-- shows the state, and the audit log is the history.
CREATE TABLE radius_operations (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    kind   text NOT NULL CHECK (kind IN ('sync', 'apply')),
    at     timestamptz NOT NULL,
    record jsonb NOT NULL,
    PRIMARY KEY (tenant_id, kind)
);

ALTER TABLE network_settings         ENABLE ROW LEVEL SECURITY;
ALTER TABLE network_certificates     ENABLE ROW LEVEL SECURITY;
ALTER TABLE radius_client_coa_timing ENABLE ROW LEVEL SECURITY;
ALTER TABLE radius_operations        ENABLE ROW LEVEL SECURITY;
ALTER TABLE network_settings         FORCE ROW LEVEL SECURITY;
ALTER TABLE network_certificates     FORCE ROW LEVEL SECURITY;
ALTER TABLE radius_client_coa_timing FORCE ROW LEVEL SECURITY;
ALTER TABLE radius_operations        FORCE ROW LEVEL SECURITY;

CREATE POLICY network_settings_isolation ON network_settings
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY network_certificates_isolation ON network_certificates
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY radius_client_coa_timing_isolation ON radius_client_coa_timing
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY radius_operations_isolation ON radius_operations
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE
    ON network_settings, network_certificates, radius_client_coa_timing, radius_operations
    TO foyer_app;

-- +goose Down

DROP TABLE radius_operations;
DROP TABLE radius_client_coa_timing;
DROP TABLE network_certificates;
DROP TABLE network_settings;
