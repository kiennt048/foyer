-- The guest portal's own two tables: the browser-to-MAC binding, and what the
-- venue decided.
--
-- Both are specified in internal/portal/CONTRACT.md, which is the authority for
-- this migration in the same way docs/architecture.md is the authority for 00004.
-- Three of the properties below are security properties rather than shapes, and
-- each one is written next to the thing that carries it.
--
-- Identifiers are `text` here for the same reason 00003 gives: every id in this
-- schema is text, the contract types them as strings, and portal_grants.id is
-- `grant_<32 hex>` from internal/portal's own generator rather than a uuid. The
-- CONTRACT's DDL writes `uuid` throughout; a uuid column here could not reference
-- sites, radius_clients, vouchers or accounts at all, since none of those has a
-- uuid primary key.
--
-- portal_grants is created first because portal_sessions.grant_id references it.

-- +goose Up

-- ---------------------------------------------------------------------------
-- portal_grants -- what the venue decided.
-- ---------------------------------------------------------------------------
--
-- A grant is not a session. radacct records what the network observed; this
-- records what the venue decided, and the two do not have the same lifetime: a
-- grant exists the instant a voucher is claimed, before any accounting row can,
-- and on the controller-API path there may be no accounting row ever.
CREATE TABLE portal_grants (
    id        text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    -- The clients.conf row this device's Access-Requests arrive from, where one is
    -- known. NULL for an access point behind a proxying controller, which 00004
    -- deliberately leaves unbound rather than guessing.
    nas_id    text REFERENCES radius_clients(id) ON DELETE SET NULL,
    -- macaddr, so the twelve hex digits are one value whatever the vendor spelled
    -- them as. It is still a weak identifier: iOS 14+ and Android 10+ present a
    -- per-SSID private address, so this is a pseudonym rather than a device.
    client_mac macaddr NOT NULL,
    ap_mac     macaddr,
    method     text NOT NULL CHECK (method IN ('voucher', 'account', 'click-through')),
    voucher_id text REFERENCES vouchers(id) ON DELETE SET NULL,
    account_id text REFERENCES accounts(id) ON DELETE SET NULL,
    -- The plan, copied. There is deliberately no foreign key to the batch or the
    -- account it came from: a batch edited afterwards must not change what a guest
    -- was already sold.
    plan_name         text,
    plan_name_lang    text,
    duration_minutes  integer,
    data_allowance_mb integer,
    up_kbps           integer,
    down_kbps         integer,
    -- Stored, never derived at read time. It is the driver's HardQuota capability
    -- as it was at the moment this guest was let on; re-deriving it later would
    -- report today's capabilities against last week's grant while the number
    -- beside it had not changed.
    enforced_by text NOT NULL
                CHECK (enforced_by IN ('nas', 'foyer-sampled', 'unenforced')),
    granted_at timestamptz NOT NULL DEFAULT now(),
    -- Expiry is derived at read time, not stored as a state: the clock passing a
    -- number needs nothing to happen for it to be true. Revocation is an event and
    -- is written when it happens.
    expires_at timestamptz,
    revoked_at timestamptz,

    CHECK ((method = 'voucher') = (voucher_id IS NOT NULL)),
    CHECK ((method = 'account') = (account_id IS NOT NULL))
);

CREATE INDEX portal_grants_by_mac ON portal_grants (site_id, client_mac, granted_at DESC);

-- Single use, as a constraint rather than only as a transaction.
--
-- docs/rules/security.md §9 asks for single use "enforced transactionally, not with a
-- read-then-write". The claim is one UPDATE ... WHERE state = 'unused', which is
-- that; this index is the version of the guarantee that survives a future caller
-- who writes the read-then-write anyway.
CREATE UNIQUE INDEX portal_grants_one_per_voucher
    ON portal_grants (voucher_id) WHERE voucher_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- portal_sessions -- one browser, bound to one MAC.
-- ---------------------------------------------------------------------------
--
-- The primary key is the token hash and there is no lookup by MAC, which is the
-- whole of docs/rules/security.md §4 expressed as a schema: a lookup by MAC is a
-- lookup by a value the NAS supplies unsigned, and resolving a browser's state from
-- a parameter somebody typed is the failure the rule names. There is no index on
-- client_mac either, deliberately -- an index is an invitation.
--
-- The token itself is never stored, only sha256 of it, for the same reason
-- operator_sessions stores a hash: a database dump must not contain live sessions.
CREATE TABLE portal_sessions (
    token_hash text PRIMARY KEY,
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                    DEFAULT foyer_current_tenant(),
    site_id    text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    nas_id     text REFERENCES radius_clients(id) ON DELETE SET NULL,
    -- The controller's own site identifier, which is what VerifyPresence and
    -- AuthorizeGuest take. It is not the Foyer site id, and conflating the two is
    -- how a driver ends up looking a client up in the wrong place.
    vendor_site_id text,
    client_mac macaddr NOT NULL,
    ap_mac     macaddr,
    ssid       text,
    -- Allowlisted on the way in, not only on the way out. A value that never
    -- enters the database in a dangerous shape cannot be rendered in one by a
    -- future reader who forgets to check.
    original_url text,
    grant_id     text REFERENCES portal_grants(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- The pre-authentication time limit docs/rules/security.md §5 asks for. There is no
    -- identity behind this row, so nothing else bounds it. Enforced on read as
    -- well as by whatever reaps the table: a row past its bound answers the same
    -- not-found a token that never existed gets, because telling them apart would
    -- say whether a token was ever valid.
    expires_at timestamptz NOT NULL
);

CREATE INDEX portal_sessions_expiry ON portal_sessions (expires_at);

ALTER TABLE portal_grants   ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_sessions ENABLE ROW LEVEL SECURITY;

CREATE POLICY portal_grants_isolation ON portal_grants
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY portal_sessions_isolation ON portal_sessions
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE ON portal_grants, portal_sessions TO foyer_app;

-- +goose Down

DROP TABLE portal_sessions;
DROP TABLE portal_grants;
