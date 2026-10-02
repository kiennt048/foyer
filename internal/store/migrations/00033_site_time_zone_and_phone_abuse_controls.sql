-- The admin-configurability rule: a site's calendar, phone
-- sign-in's abuse limits and destination ranges, and a deployment-wide SMS cap
-- become settings an operator changes in the console rather than constants or an
-- environment variable nobody set.
--
-- Every value here is read per request by the process that enforces it, so a
-- write takes effect on the next request; nothing is cached across a restart and
-- nothing needs one.

-- +goose Up

-- ---------------------------------------------------------------------------
-- sites.time_zone -- where "today" starts for this venue.
-- ---------------------------------------------------------------------------
--
-- The day boundary is the site's own zone rather than `time.Local`, which is
-- whatever `TZ` says in the container. The default is Asia/Ho_Chi_Minh, so a site
-- counts the day its operator means. The name is
-- validated against the zone database by the API (Go's `time.LoadLocation`); the
-- CHECK only bounds its shape, because the database has no zone list to consult.
ALTER TABLE sites ADD COLUMN time_zone text NOT NULL DEFAULT 'Asia/Ho_Chi_Minh'
    CHECK (time_zone ~ '^[A-Za-z][A-Za-z0-9_+/-]{0,63}$' AND time_zone <> 'Local');

-- ---------------------------------------------------------------------------
-- phone_abuse_controls -- one venue's limits, within the product's bounds.
-- ---------------------------------------------------------------------------
--
-- No row is the product defaults (internal/identity/otp.DefaultLimits). Every
-- bound below is the contract's `PhoneAbuseLimitsUpdate` restated as a constraint,
-- so a write that skipped the API still cannot switch a limit off: there is no
-- NULL and no value that means "unlimited". code_minutes and max_attempts stop at
-- ten, NIST SP 800-63B §5.1.3's bound for an out-of-band code.
CREATE TABLE phone_abuse_controls (
    site_id   text PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    per_number_hour  integer NOT NULL CHECK (per_number_hour BETWEEN 1 AND 10),
    per_number_day   integer NOT NULL CHECK (per_number_day BETWEEN 1 AND 20),
    per_device_hour  integer NOT NULL CHECK (per_device_hour BETWEEN 1 AND 20),
    per_address_hour integer NOT NULL CHECK (per_address_hour BETWEEN 1 AND 2000),
    code_minutes     integer NOT NULL CHECK (code_minutes BETWEEN 1 AND 10),
    max_attempts     integer NOT NULL CHECK (max_attempts BETWEEN 1 AND 10),
    -- A narrowing of the Vietnamese mobile plan, never a widening of it.
    mobile_prefixes text[] NOT NULL
        CHECK (cardinality(mobile_prefixes) BETWEEN 1 AND 5
               AND mobile_prefixes <@ ARRAY['03', '05', '07', '08', '09']::text[]),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CHECK (per_number_day >= per_number_hour)
);

-- ---------------------------------------------------------------------------
-- phone_spend_cap and phone_code_sends_total -- §9's global cap.
-- ---------------------------------------------------------------------------
--
-- One row per tenant. daily_limit NULL is "no deployment-wide cap": every site's
-- own ceiling is NOT NULL (00022), so the sum of them already bounds the spend and
-- this is a second, tighter bound an operator may add.
CREATE TABLE phone_spend_cap (
    tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    daily_limit integer CHECK (daily_limit BETWEEN 1 AND 1000000),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- The global counter, incremented in the same transaction as the per-site one
-- (PhoneRepo.Issue) whether or not a cap is set, so a cap set at noon counts the
-- morning. The day is the sending site's calendar day. Names nobody.
CREATE TABLE phone_code_sends_total (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    day  date NOT NULL,
    sent integer NOT NULL CHECK (sent >= 0),
    PRIMARY KEY (tenant_id, day)
);

ALTER TABLE phone_abuse_controls   ENABLE ROW LEVEL SECURITY;
ALTER TABLE phone_spend_cap        ENABLE ROW LEVEL SECURITY;
ALTER TABLE phone_code_sends_total ENABLE ROW LEVEL SECURITY;
ALTER TABLE phone_abuse_controls   FORCE ROW LEVEL SECURITY;
ALTER TABLE phone_spend_cap        FORCE ROW LEVEL SECURITY;
ALTER TABLE phone_code_sends_total FORCE ROW LEVEL SECURITY;

CREATE POLICY phone_abuse_controls_isolation ON phone_abuse_controls
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY phone_spend_cap_isolation ON phone_spend_cap
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY phone_code_sends_total_isolation ON phone_code_sends_total
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE ON phone_abuse_controls, phone_spend_cap, phone_code_sends_total TO foyer_app;

-- +goose Down

DROP TABLE phone_code_sends_total;
DROP TABLE phone_spend_cap;
DROP TABLE phone_abuse_controls;
ALTER TABLE sites DROP COLUMN time_zone;
