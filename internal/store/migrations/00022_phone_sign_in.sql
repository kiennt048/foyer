-- Phone sign-in: a six-digit code sent to a Vietnamese mobile number.
--
-- Three tables and two widened constraints. The reasoning for the code's storage
-- is internal/identity/otp/code.go; the abuse controls are internal/identity/otp's
-- package comment. Each property below that is a security property rather than a
-- shape is written beside the thing that carries it.
--
-- PERSONAL DATA. `phone_codes.phone`, `phone_codes.client_mac` and
-- `portal_grants.phone` are personal data under Luật 91/2025/QH15. The rows in
-- phone_codes are worth nothing after their five-minute lifetime except as the
-- per-number counter, which reads the last 24 hours only, so they are safe to
-- delete after a day; the retention policy owns doing so.

-- +goose Up

-- ---------------------------------------------------------------------------
-- The grant and the authorization event learn a fourth method.
-- ---------------------------------------------------------------------------
ALTER TABLE portal_grants DROP CONSTRAINT portal_grants_method_check;
ALTER TABLE portal_grants ADD CONSTRAINT portal_grants_method_check
    CHECK (method IN ('voucher', 'account', 'click-through', 'phone'));

-- The identity a phone guest presented, E.164. It is on the grant for the reason
-- docs/rules/security.md §6 gives: bind grants to identity, with MAC as a hint.
ALTER TABLE portal_grants ADD COLUMN phone text
    CHECK (phone ~ '^\+[1-9][0-9]{7,14}$');
ALTER TABLE portal_grants ADD CONSTRAINT portal_grants_phone_matches_method
    CHECK ((method = 'phone') = (phone IS NOT NULL));

ALTER TABLE authorization_events DROP CONSTRAINT authorization_events_method_check;
ALTER TABLE authorization_events ADD CONSTRAINT authorization_events_method_check
    CHECK (method IN ('voucher', 'account', 'click-through', 'phone'));

-- ---------------------------------------------------------------------------
-- phone_sign_in -- how one site sends codes.
-- ---------------------------------------------------------------------------
--
-- The provider's keys are sealed like a controller credential (envelope.go): AES-
-- GCM under a data key wrapped by FOYER_KEK, bound to this table, column and row.
-- foyer_radius has no grant on this table, and the admin API never returns them.
CREATE TABLE phone_sign_in (
    site_id   text PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    provider  text NOT NULL CHECK (provider IN ('esms-sms', 'esms-zalo')),
    brandname        text,
    zalo_oa_id       text,
    zalo_template_id text,
    api_key    bytea NOT NULL,
    secret_key bytea NOT NULL,
    sandbox    boolean NOT NULL DEFAULT false,
    -- The SMS cost ceiling. NOT NULL with no default: a venue decides what it is
    -- willing to spend, and there is no value of "unlimited".
    daily_limit     integer NOT NULL CHECK (daily_limit BETWEEN 1 AND 100000),
    session_minutes integer NOT NULL CHECK (session_minutes BETWEEN 1 AND 10080),
    message_template text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),

    CHECK (provider <> 'esms-sms'  OR brandname IS NOT NULL),
    CHECK (provider <> 'esms-zalo' OR (zalo_oa_id IS NOT NULL AND zalo_template_id IS NOT NULL))
);

-- ---------------------------------------------------------------------------
-- phone_codes -- one code sent to one number for one browser.
-- ---------------------------------------------------------------------------
--
-- **The code is not here.** code_mac is HMAC-SHA256 under a key derived from
-- FOYER_KEK (store/phonecodekey.go) over the row id, the session's token hash and
-- the code; key_id names the keyring entry that made it. A dump of this table is
-- a list of MACs nobody can invert without a key that is by rule not in the
-- database.
--
-- session_token_hash deliberately has **no foreign key** to portal_sessions. A
-- cascade would delete a browser's codes when its session is forgotten on a MAC
-- change — and with them the rows the per-number limit counts, which would make
-- "forget my session" a way to reset the limit.
CREATE TABLE phone_codes (
    id        text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    session_token_hash text NOT NULL,
    client_mac macaddr NOT NULL,
    phone      text NOT NULL CHECK (phone ~ '^\+[1-9][0-9]{7,14}$'),
    code_mac   bytea NOT NULL CHECK (octet_length(code_mac) = 32),
    key_id     text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    attempts     integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL CHECK (max_attempts >= 1),
    -- Used, retired by a newer code, or retired because the send failed.
    consumed_at timestamptz,

    CHECK (expires_at > created_at),
    -- The attempt bound as a constraint as well as a WHERE clause: an UPDATE that
    -- forgot `attempts < max_attempts` fails here rather than granting a sixth
    -- guess.
    CHECK (attempts BETWEEN 0 AND max_attempts)
);

CREATE INDEX phone_codes_by_session ON phone_codes (session_token_hash, created_at DESC);
-- The per-number limit counts codes to one number at one site over the last day.
CREATE INDEX phone_codes_by_number ON phone_codes (site_id, phone, created_at DESC);

-- ---------------------------------------------------------------------------
-- phone_code_sends -- the daily ceiling's counter.
-- ---------------------------------------------------------------------------
--
-- One row per site per calendar day, incremented by a conditional upsert that
-- refuses at the limit, so two sends in the same millisecond cannot both fit under
-- the last unit of budget. A counter rather than a COUNT over phone_codes because
-- phone_codes is personal data with a short retention, and the ceiling must not
-- reset when it is reaped.
CREATE TABLE phone_code_sends (
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    day  date NOT NULL,
    sent integer NOT NULL CHECK (sent >= 0),
    PRIMARY KEY (site_id, day)
);

ALTER TABLE phone_sign_in    ENABLE ROW LEVEL SECURITY;
ALTER TABLE phone_codes      ENABLE ROW LEVEL SECURITY;
ALTER TABLE phone_code_sends ENABLE ROW LEVEL SECURITY;
-- Forced, as 00019 forces every table with a policy, so the owner is not exempt
-- (`TestEveryRowLevelPolicyIsForced`).
ALTER TABLE phone_sign_in    FORCE ROW LEVEL SECURITY;
ALTER TABLE phone_codes      FORCE ROW LEVEL SECURITY;
ALTER TABLE phone_code_sends FORCE ROW LEVEL SECURITY;

CREATE POLICY phone_sign_in_isolation ON phone_sign_in
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY phone_codes_isolation ON phone_codes
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY phone_code_sends_isolation ON phone_code_sends
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE ON phone_sign_in, phone_codes, phone_code_sends TO foyer_app;

-- +goose Down

DROP TABLE phone_code_sends;
DROP TABLE phone_codes;
DROP TABLE phone_sign_in;

ALTER TABLE authorization_events DROP CONSTRAINT authorization_events_method_check;
ALTER TABLE authorization_events ADD CONSTRAINT authorization_events_method_check
    CHECK (method IN ('voucher', 'account', 'click-through'));
ALTER TABLE portal_grants DROP CONSTRAINT portal_grants_phone_matches_method;
ALTER TABLE portal_grants DROP COLUMN phone;
ALTER TABLE portal_grants DROP CONSTRAINT portal_grants_method_check;
ALTER TABLE portal_grants ADD CONSTRAINT portal_grants_method_check
    CHECK (method IN ('voucher', 'account', 'click-through'));
