-- The correction this migration makes: "access point" was not one wrong noun, it was
-- three nouns fused into one.
--
--   controller     the management plane Foyer holds credentials for. Identified by a
--                  URL. Created by the operator, once.
--   radius client  a row in clients.conf: an address or CIDR, a shared secret,
--                  require_message_authenticator. Identified by an address. Derived.
--   access point   a radio a guest is standing under. Identified by a MAC. It is the
--                  MAC the NAS redirect carries, and it is the identifier the old
--                  model had nowhere to put. Discovered.
--
-- Their counts differ, their identifiers differ, and their failure states differ:
-- one controller, one shared secret, twelve client addresses, twelve access points.
--
-- The arithmetic that forces `shared_secrets` to be its own table is measured, not
-- assumed. From docs/protocol-reality.md: "Every access point is its own RADIUS
-- client, but they share one shared secret. The secret is a property of a server
-- entry inside a controller-level RADIUS profile; the client address is each AP's
-- own. So clients.conf needs N addresses (or a CIDR) against one secret." A secret
-- column on the client row would be N secrets against a controller that has one
-- field to type one into, and the registration wizard's sentence "Enter it on the
-- device" would have no referent.
--
-- See docs/architecture.md, which is the authority for this migration.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Controller -- the thing an operator possesses and configures.
-- ---------------------------------------------------------------------------
CREATE TABLE controllers (
    id        text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    name      text NOT NULL,
    vendor    text NOT NULL CHECK (vendor IN ('unifi', 'omada', 'ruckus')),
    base_url  text NOT NULL,
    -- Omada only: the 32-character identifier that appears in the controller's own
    -- address bar and cannot be derived or guessed. Its absence is the reason a
    -- first Omada connection attempt fails with nothing useful to say.
    vendor_controller_id text,
    auth_mode text NOT NULL
              CHECK (auth_mode IN ('api-key', 'local-admin', 'hotspot-operator')),
    tls_mode  text NOT NULL CHECK (tls_mode IN ('verify', 'trust-on-first-use')),
    -- Recorded on first contact when tls_mode is trust-on-first-use. Controllers
    -- ship self-signed certificates; pinning what was seen is the only honest
    -- alternative to turning verification off and not saying so.
    certificate_fingerprint text,
    -- The half of the credential that is not a secret. NULL for api-key.
    username  text,
    -- Envelope-encrypted: AES-256-GCM data key per row, wrapped under a key that is
    -- not in this database. Holds the API key, or the password for the two
    -- username-bearing auth modes. NULL means no credential has been set, which is
    -- what `Controller.credentialSet` reports.
    credential bytea,
    reachability text NOT NULL DEFAULT 'unknown'
                 CHECK (reachability IN ('ok', 'unreachable', 'credentials-expired', 'unknown')),
    version           text,
    version_supported boolean,
    -- UniFi OS vs standalone, Omada software vs hardware. Probed, never asked.
    flavour           text,
    last_contact_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

-- The controller's own sites, and which Foyer site each is bound to.
--
-- PacketFence's UniFi integration enumerates sites and searches them for the client;
-- a hardcoded `default` site is a filed bug there (inverse-inc/packetfence#4280).
-- Enumerating them into rows is what makes the binding an operator decision instead
-- of an assumption.
CREATE TABLE controller_vendor_sites (
    controller_id  text NOT NULL REFERENCES controllers(id) ON DELETE CASCADE,
    vendor_site_id text NOT NULL,
    tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                        DEFAULT foyer_current_tenant(),
    name           text NOT NULL,
    bound_site_id  text REFERENCES sites(id) ON DELETE SET NULL,
    PRIMARY KEY (controller_id, vendor_site_id)
);

-- At most one binding per Foyer site: a site whose vouchers are authorised by two
-- controllers has no answer to "which one revokes this grant".
CREATE UNIQUE INDEX controller_vendor_sites_bound_idx
    ON controller_vendor_sites (bound_site_id) WHERE bound_site_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Shared secret -- one per RADIUS profile, many clients.
-- ---------------------------------------------------------------------------
CREATE TABLE shared_secrets (
    id        text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    -- What the operator pasted this into: "UniFi RADIUS profile -- Guest".
    label     text NOT NULL,
    -- Envelope-encrypted, same construction as controllers.credential. The cleartext
    -- projection FreeRADIUS reads is `nas.secret` (migration 00002).
    secret     bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- Rotation affects a *set* of clients, because the secret is one per profile.
    -- Stating that is the point of the table.
    last_rotated_at timestamptz,
    last_used_at    timestamptz
);

CREATE INDEX shared_secrets_site_idx ON shared_secrets (site_id);

-- ---------------------------------------------------------------------------
-- RADIUS client -- a clients.conf row.
-- ---------------------------------------------------------------------------
CREATE TABLE radius_clients (
    id        text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    name      text NOT NULL,
    description text,
    -- `inet`, not `cidr`: this is one address or a range covering a fleet, and cidr
    -- rejects an address with host bits set. Aruba's dynamic-radius-proxy toggle
    -- decides which of the two an operator needs, and Foyer cannot see that toggle.
    address   inet NOT NULL,
    source    text NOT NULL CHECK (source IN ('discovered', 'manual')),
    driver    text NOT NULL CHECK (driver IN ('unifi', 'mikrotik', 'coa', 'ruckus', 'omada')),
    controller_id    text REFERENCES controllers(id) ON DELETE SET NULL,
    shared_secret_id text REFERENCES shared_secrets(id) ON DELETE RESTRICT,
    nas_identifier   text,
    -- Per-NAS, not a constant. 3799 is common; Cisco frequently uses 1700.
    coa_port integer CHECK (coa_port IS NULL OR (coa_port > 0 AND coa_port < 65536)),
    -- What the generator must emit for this client. Both default to the safe value
    -- rather than FreeRADIUS's `auto`, which silently leaves legacy gear unprotected
    -- against BlastRADIUS (CVE-2024-3596).
    require_message_authenticator boolean NOT NULL DEFAULT true,
    limit_proxy_state             boolean NOT NULL DEFAULT true,
    -- What was actually observed on the wire, which is a different claim from what
    -- was configured above, and is the one the console raises an alarm on.
    message_authenticator text NOT NULL DEFAULT 'unknown'
                          CHECK (message_authenticator IN ('enforced', 'refused', 'unknown')),
    -- Three-valued: NULL is "not measured", and the console must not render it as
    -- "missing". On the controller-API path there is no Access-Accept to carry
    -- Acct-Interim-Interval, so `false` here means every byte figure for this client
    -- is absent forever -- and only that. Who is authorised, since when and until
    -- when still come from the controller.
    accounting_configured boolean,
    reachability text NOT NULL DEFAULT 'unknown'
                 CHECK (reachability IN ('ok', 'unreachable', 'credentials-expired', 'unknown')),
    -- The contract's Capabilities object, stored where the console reads it.
    -- Deliberately one home: a copy on the controller as well would be a second
    -- source of truth for a value that gates whether controls exist at all.
    capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled      boolean NOT NULL DEFAULT true,
    last_request_at timestamptz,
    probed_at       timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Two clients on one address is two clients.conf rows FreeRADIUS resolves by
    -- accident of ordering.
    UNIQUE (tenant_id, address)
);

CREATE INDEX radius_clients_site_idx       ON radius_clients (site_id);
CREATE INDEX radius_clients_controller_idx ON radius_clients (controller_id);
CREATE INDEX radius_clients_secret_idx     ON radius_clients (shared_secret_id);

-- ---------------------------------------------------------------------------
-- Access point -- discovered, never typed.
-- ---------------------------------------------------------------------------
CREATE TABLE access_points (
    id        text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    controller_id text REFERENCES controllers(id) ON DELETE CASCADE,
    -- `macaddr`, so `94:2a:6f:d0:30:57`, `94-2a-6f-d0-30-57` and `942a6fd03057` are
    -- one value rather than three. The redirect carries this, PacketFence's
    -- radiusDisconnect() sets NAS-Identifier from it with the colons stripped, and
    -- the old model stored it nowhere.
    mac       macaddr NOT NULL,
    name      text NOT NULL,
    model     text,
    firmware  text,
    ip_address inet,
    -- The address this AP speaks RADIUS from -- resolved from the controller's
    -- device list, which is the only thing that can resolve it.
    radius_client_id text REFERENCES radius_clients(id) ON DELETE SET NULL,
    radius_via text NOT NULL DEFAULT 'self' CHECK (radius_via IN ('self', 'controller')),
    state text NOT NULL DEFAULT 'unknown'
          CHECK (state IN ('online', 'offline', 'missing', 'unknown')),
    -- An AP that disappeared from discovery is not an AP that was deleted, and the
    -- console must be able to say which. `missing` is set by reconciliation and
    -- cleared when the AP comes back; the row survives either way, because deleting
    -- it would drop the MAC that resolves last week's session rows.
    missing_since timestamptz,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, mac)
);

CREATE INDEX access_points_site_idx          ON access_points (site_id);
CREATE INDEX access_points_controller_idx    ON access_points (controller_id);
CREATE INDEX access_points_radius_client_idx ON access_points (radius_client_id);

ALTER TABLE controllers            ENABLE ROW LEVEL SECURITY;
ALTER TABLE controller_vendor_sites ENABLE ROW LEVEL SECURITY;
ALTER TABLE shared_secrets         ENABLE ROW LEVEL SECURITY;
ALTER TABLE radius_clients         ENABLE ROW LEVEL SECURITY;
ALTER TABLE access_points          ENABLE ROW LEVEL SECURITY;

CREATE POLICY controllers_isolation ON controllers
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY controller_vendor_sites_isolation ON controller_vendor_sites
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY shared_secrets_isolation ON shared_secrets
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY radius_clients_isolation ON radius_clients
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY access_points_isolation ON access_points
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE
    ON controllers, controller_vendor_sites, shared_secrets, radius_clients, access_points
    TO foyer_app;

-- +goose Down

DROP TABLE access_points;
DROP TABLE radius_clients;
DROP TABLE shared_secrets;
DROP TABLE controller_vendor_sites;
DROP TABLE controllers;
