-- What the guest sees, and what a guest who has not authenticated yet may reach.

-- +goose Up

CREATE TABLE portal_settings (
    site_id   text PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    -- Where the portal answers. NULL until a deployment knows its own address, and
    -- the console must say so rather than guessing: this value is what has to be in
    -- the walled garden, so a wrong one is a portal that cannot serve itself.
    portal_url   text,
    venue_name   text NOT NULL,
    network_name text,
    -- Inlined into the document as a data URI, never linked. The portal must render
    -- before the client has internet, Android calls clearCache(true) on every portal
    -- launch, and a linked asset is a second request that will not complete.
    logo_data_uri text,
    languages text[] NOT NULL DEFAULT ARRAY['en']::text[]
              CHECK (array_length(languages, 1) >= 1),
    methods   text[] NOT NULL DEFAULT ARRAY['voucher']::text[]
              CHECK (array_length(methods, 1) >= 1),
    click_through_minutes integer CHECK (click_through_minutes IS NULL OR click_through_minutes >= 1),
    -- Language code to the venue's terms in that language. A map rather than a
    -- string because terms are legal text: the portal never paraphrases them, never
    -- machine-translates them, and never silently substitutes one language for
    -- another. Foyer's own consent sentence is not in here and cannot be put in
    -- here -- it is rendered unconditionally beneath whatever resolves.
    terms jsonb NOT NULL DEFAULT '{"summary":{}}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- The venue photograph, stored rather than linked, for the same reason as the logo.
-- One row per site: uploading a second replaces the first.
--
-- It is a separate table so that reading portal settings -- which the guest portal
-- does on every document -- does not drag a few hundred kilobytes of JPEG through
-- the connection pool.
CREATE TABLE portal_hero_images (
    site_id      text PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                      DEFAULT foyer_current_tenant(),
    content_type text NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png', 'image/webp')),
    bytes        integer NOT NULL CHECK (bytes > 0),
    data         bytea NOT NULL,
    uploaded_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE walled_garden_entries (
    id        text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    -- Two values, not three, and the difference is not a matter of degree. `fqdn` is
    -- matched by name via SNI and allows one destination; `ip-range` is matched by
    -- address and a CDN range hosts arbitrary customers, so an entry meant to allow
    -- one service allows everything behind the same edge.
    kind  text NOT NULL CHECK (kind IN ('fqdn', 'ip-range')),
    value text NOT NULL,
    note  text,
    -- `required` entries are the ones without which the portal cannot serve itself:
    -- its own FQDN, its certificate chain's OCSP and CRL endpoints, the RFC 8908
    -- endpoint. They are not the operator's to delete.
    origin text NOT NULL DEFAULT 'operator' CHECK (origin IN ('required', 'operator')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, kind, value)
);

CREATE INDEX walled_garden_entries_site_idx ON walled_garden_entries (site_id);

ALTER TABLE portal_settings       ENABLE ROW LEVEL SECURITY;
ALTER TABLE portal_hero_images    ENABLE ROW LEVEL SECURITY;
ALTER TABLE walled_garden_entries ENABLE ROW LEVEL SECURITY;

CREATE POLICY portal_settings_isolation ON portal_settings
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY portal_hero_images_isolation ON portal_hero_images
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY walled_garden_entries_isolation ON walled_garden_entries
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE
    ON portal_settings, portal_hero_images, walled_garden_entries TO foyer_app;

-- +goose Down

DROP TABLE walled_garden_entries;
DROP TABLE portal_hero_images;
DROP TABLE portal_settings;
