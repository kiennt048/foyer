-- The guest network's Wi-Fi password, for the voucher slip's QR code (the
-- admin-configurability rule).
--
-- A venue that runs its guest SSID behind WPA as well as the portal printed slips
-- whose QR joined an *open* network: `PortalSettings` had no field for the
-- passphrase, so no phone could use the code. One row per site, set in the console.
--
-- **Sealed like every other secret here** (envelope.go): AES-GCM under a data key
-- wrapped by FOYER_KEK, bound to this table, column and site. It is not a
-- credential that authenticates anyone to Foyer -- it is printed on paper handed to
-- guests -- but it is an operator's secret all the same, the settings API never
-- returns it, and one rule for every secret is easier to keep than a judgement per
-- column. foyer_radius has no grant on this table.

-- +goose Up

CREATE TABLE guest_wifi_keys (
    site_id    text PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                    DEFAULT foyer_current_tenant(),
    passphrase bytea NOT NULL,
    updated_by text,
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE guest_wifi_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE guest_wifi_keys FORCE ROW LEVEL SECURITY;

CREATE POLICY guest_wifi_keys_isolation ON guest_wifi_keys
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE ON guest_wifi_keys TO foyer_app;

-- +goose Down

DROP TABLE guest_wifi_keys;
