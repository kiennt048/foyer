-- The RFC 8908 address book, persisted.
--
-- The Captive Portal API client is the operating system's, not the portal's
-- WebView: it carries no cookie, and RFC 8908 §4 expects the server to know it by
-- the address it asks from. internal/portal kept that book in the process's memory,
-- so a restart, or a second control-plane process, answered
-- every device `captive: true` until it next reached the front door. That is the
-- fail-safe direction and it stays the fail-safe; this table is what lets a
-- restarted or second process answer the device that is in fact online.
--
-- One row per address. What a row lets anybody learn is what the in-process book
-- let them learn: whether the device at that address is captive and how long its
-- grant has left. The MAC and the grant are here so that the reader can re-check
-- the grant is that device's; nothing returns them to a caller.
--
-- Personal data (the MAC, and the address itself): erased by MAC with the other
-- guest tables (store.RegisterEraser), with the grant by foreign-key cascade, and
-- deleted a window after it was last seen by the portal-session job
-- (store.PortalAddressRepo.Reap). docs/operator/personal-data.md lists both.

-- +goose Up
CREATE TABLE portal_addresses (
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                    DEFAULT foyer_current_tenant(),
    -- The transport source of a completed connection, never a forwarding header.
    address    inet NOT NULL,
    token_hash text NOT NULL,
    client_mac macaddr NOT NULL,
    -- A grant's record going takes the address's claim to it with it.
    grant_id   text REFERENCES portal_grants(id) ON DELETE CASCADE,
    seen_at    timestamptz NOT NULL,
    -- A second device was seen from this address inside the window: until this
    -- instant the address means nobody.
    ambiguous_until timestamptz,
    PRIMARY KEY (tenant_id, address)
);

CREATE INDEX portal_addresses_by_token ON portal_addresses (token_hash);
CREATE INDEX portal_addresses_by_seen ON portal_addresses (seen_at);

ALTER TABLE portal_addresses ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_addresses FORCE ROW LEVEL SECURITY;
CREATE POLICY portal_addresses_isolation ON portal_addresses
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE ON portal_addresses TO foyer_app;

-- +goose Down
DROP TABLE portal_addresses;
