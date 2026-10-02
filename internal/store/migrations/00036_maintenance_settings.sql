-- The control plane's maintenance settings, in the database (the
-- admin-configurability rule).
--
-- Until this migration the interval was FOYER_MAINTENANCE_INTERVAL, read once at
-- start, and the reaper's ceilings and the retention batch were constants: three
-- operator settings reachable only by editing a container's environment or the
-- source. They are one row per tenant now, read by internal/jobs every cycle and
-- written by `updateMaintenanceSettings`.
--
-- **No row means the defaults.** The defaults and the bounds live in Go
-- (internal/store/maintenance.go, MaintenanceLimits), beside the code that uses
-- them, exactly as a retention category's do; the CHECKs below repeat the bounds
-- as the last line of defence rather than as a second source the console reads.

-- +goose Up

CREATE TABLE maintenance_settings (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    interval_seconds           integer NOT NULL CHECK (interval_seconds BETWEEN 60 AND 86400),
    stale_after_missed_updates integer NOT NULL CHECK (stale_after_missed_updates BETWEEN 3 AND 48),
    stale_ceiling_per_pass     integer NOT NULL CHECK (stale_ceiling_per_pass BETWEEN 1 AND 100000),
    retention_batch_size       integer NOT NULL CHECK (retention_batch_size BETWEEN 50 AND 10000),
    -- Denormalised, like retention_policies.updated_by: it has to outlive the
    -- operator it names.
    updated_by text,
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE maintenance_settings ENABLE ROW LEVEL SECURITY;
-- Forced, as 00019 forces every table with a policy (`TestEveryRowLevelPolicyIsForced`).
ALTER TABLE maintenance_settings FORCE ROW LEVEL SECURITY;

CREATE POLICY maintenance_settings_isolation ON maintenance_settings
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE ON maintenance_settings TO foyer_app;

-- +goose Down

DROP TABLE maintenance_settings;
