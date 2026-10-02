-- Guest identities: voucher batches, vouchers, accounts, and the one table that
-- holds password-equivalent material.
--
-- The plan (duration, allowance, rate limits) is stored inline on the batch and on
-- the account rather than in a `plans` table. There is no operation in the contract
-- that names a plan on its own, nothing edits one after issue, and a shared plan row
-- would let editing it change what a voucher sold last month promised.

-- +goose Up

CREATE TABLE voucher_batches (
    id        text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    label     text NOT NULL,
    count     integer NOT NULL CHECK (count > 0),
    created_by text,
    -- The plan, inline.
    plan_name         text,
    plan_name_lang    text CHECK (plan_name_lang IS NULL OR plan_name_lang IN ('en', 'vi')),
    duration_minutes  integer,
    data_allowance_mb integer,
    down_kbps         integer,
    up_kbps           integer,
    -- Who actually applies the plan. `foyer-sampled` is the honest answer when the
    -- driver has no hard quota: radacct advances only on Interim-Update, the
    -- interval has a widely-implemented 300-second floor, and the console must say
    -- "sampled every N minutes" rather than render a cap nothing enforces.
    enforced_by text NOT NULL DEFAULT 'unenforced'
                CHECK (enforced_by IN ('nas', 'foyer-sampled', 'unenforced')),
    -- from-first-use is why a voucher printed in January is still worth selling in
    -- March, and why expires_at on the voucher is NULL until it is redeemed.
    expiry_mode text NOT NULL CHECK (expiry_mode IN ('from-creation', 'from-first-use')),
    valid_for_hours integer CHECK (valid_for_hours IS NULL OR valid_for_hours > 0),
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX voucher_batches_site_created_idx ON voucher_batches (site_id, created_at DESC);

CREATE TABLE vouchers (
    id        text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    batch_id  text NOT NULL REFERENCES voucher_batches(id) ON DELETE CASCADE,
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    -- The contract's alphabet, enforced here too. I, L, O, 0 and 1 are absent
    -- because the guest is reading this off a printed slip under bar lighting.
    code  text NOT NULL CHECK (code ~ '^[A-HJ-KM-NP-Z2-9]{12}$'),
    state text NOT NULL DEFAULT 'unused'
          CHECK (state IN ('unused', 'in-use', 'exhausted', 'expired', 'revoked')),
    first_used_at timestamptz,
    expires_at    timestamptz,
    revoked_at    timestamptz,
    -- radacct's acctuniqueid for the session currently holding this voucher, or
    -- NULL. Not a foreign key: radacct is FreeRADIUS's table and a constraint from
    -- here into it would make Foyer's schema a precondition for the data plane.
    current_session_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- The guest types the code and nothing else, so it has to resolve on its own.
    UNIQUE (tenant_id, code)
);

CREATE INDEX vouchers_batch_idx ON vouchers (batch_id);
CREATE INDEX vouchers_site_state_idx ON vouchers (site_id, state);

CREATE TABLE accounts (
    id        text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    username  text NOT NULL,
    display_name text,
    -- Argon2id, always. This table is reachable by PAP and by EAP-TTLS/PAP, both of
    -- which verify against a one-way hash. It is deliberately not the table an
    -- MSCHAPv2 lookup can reach -- see account_nt_hashes below.
    password_hash text NOT NULL,
    enabled   boolean NOT NULL DEFAULT true,
    plan_name         text,
    plan_name_lang    text CHECK (plan_name_lang IS NULL OR plan_name_lang IN ('en', 'vi')),
    duration_minutes  integer,
    data_allowance_mb integer,
    down_kbps         integer,
    up_kbps           integer,
    enforced_by text NOT NULL DEFAULT 'unenforced'
                CHECK (enforced_by IN ('nas', 'foyer-sampled', 'unenforced')),
    last_login_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, site_id, username)
);

-- The one table in this schema that holds password-equivalent material.
--
-- An NT hash is not a hash you can shrug at: it is the sole input to the MSCHAPv2
-- response, so a stolen hash authenticates directly. It is unsalted MD4 and cannot
-- be strengthened, which is physics rather than a design preference -- a
-- challenge-response protocol requires the server to hold recoverable material.
--
-- Three properties make it survivable, and all three are structural:
--
--   * **A separate table.** A row here exists only for an identity explicitly opted
--     in to PEAP-MSCHAPv2. No general account query joins it, so the ordinary path
--     cannot leak one by accident.
--   * **Envelope-encrypted** under a key that is not in this database, so a database
--     dump on its own is not a credential store.
--   * **`opted_in_at` is recorded**, because the constraint is not merely "store it
--     safely" but "never store a password in a form that lets MSCHAPv2 work without
--     saying so explicitly". The console renders `argon2id+nt` from the existence of
--     this row.
--
-- CHAP is absent from this schema on purpose and must stay absent: it would require
-- cleartext for a legacy checkbox.
CREATE TABLE account_nt_hashes (
    account_id text PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                    DEFAULT foyer_current_tenant(),
    nt_hash     bytea NOT NULL,
    opted_in_at timestamptz NOT NULL DEFAULT now(),
    opted_in_by text
);

ALTER TABLE voucher_batches    ENABLE ROW LEVEL SECURITY;
ALTER TABLE vouchers           ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts           ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_nt_hashes  ENABLE ROW LEVEL SECURITY;

CREATE POLICY voucher_batches_isolation ON voucher_batches
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY vouchers_isolation ON vouchers
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY accounts_isolation ON accounts
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY account_nt_hashes_isolation ON account_nt_hashes
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE
    ON voucher_batches, vouchers, accounts, account_nt_hashes TO foyer_app;

-- +goose Down

DROP TABLE account_nt_hashes;
DROP TABLE accounts;
DROP TABLE vouchers;
DROP TABLE voucher_batches;
