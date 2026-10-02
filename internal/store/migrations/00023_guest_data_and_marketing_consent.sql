-- What a venue asks its guests for at sign-in, what they gave, and the marketing
-- consents they ticked.
--
-- ── two records, never one ───────────────────────────────────────────────────
--
-- A guest's **details** (name, phone, e-mail) and their **marketing consent** are
-- stored apart, because they have different lawful bases and different lives:
--
--   * `guest_details` is what a guest typed into a sign-in form, bound to the grant
--     it was typed for. It is the venue's record of who it let on, it is never a
--     mailing list, and it goes when the grant goes -- the `portalGrants` retention
--     period, an erasure of the device, or the grant's account being erased all
--     remove it by `ON DELETE CASCADE`.
--   * `marketing_consents` holds a row **only when the guest ticked the box**. A
--     guest who did not has no row here, and that is the whole of the rule
--     "no consent, no marketing record". The contact details are copied onto it
--     at the moment of consent, so the consent does not depend on a grant that
--     retention removes after ninety days.
--
-- Vietnam's Decree 356/2025/ND-CP asks that consent be verifiable as to its time
-- and content. `consented_at` is the time; the content is the exact sentence shown,
-- kept in `marketing_consent_texts` under its SHA-256 and referenced by a foreign
-- key, so a consent can only name a text the venue actually published and the text
-- stays readable after the venue rewrites it.
--
-- ── withdrawal clears the contact, and keeps the evidence ────────────────────
--
-- A withdrawn consent keeps when it was given, which text, and when and by whom it
-- was withdrawn -- the venue's proof that it stopped -- and loses the name, phone
-- number and e-mail address, which have no purpose left. The CHECK below makes a
-- withdrawn row that still holds a contact impossible rather than unlikely.
--
-- ── row-level security is FORCED ─────────────────────────────────────────────
--
-- Every table here is tenant-scoped like the rest of the schema, and FORCE applies
-- the policy to the table owner as well, so the migration runner's own connection
-- cannot read across tenants either (00019's rule for every RLS table).

-- +goose Up

CREATE TABLE guest_data_settings (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    -- The contract's `GuestDataFieldSetting[]`, verbatim and in form order. The
    -- shape is the API's to enforce (`updateGuestDataSettings`), as with `design`.
    fields          jsonb   NOT NULL DEFAULT '[]'::jsonb,
    consent_enabled boolean NOT NULL DEFAULT false,
    -- `{ "en": "...", "vi": "..." }`, the text as the operator wrote it.
    consent_text    jsonb   NOT NULL DEFAULT '{}'::jsonb,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, site_id)
);

CREATE TABLE marketing_consent_texts (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    version   text NOT NULL CHECK (version ~ '^[0-9a-f]{64}$'),
    lang      text NOT NULL CHECK (lang IN ('en', 'vi')),
    body      text NOT NULL,
    first_published_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, site_id, version)
);

CREATE TABLE guest_details (
    grant_id  text PRIMARY KEY REFERENCES portal_grants(id) ON DELETE CASCADE,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    name      text CHECK (length(name)  <= 120),
    phone     text CHECK (length(phone) <= 32),
    email     text CHECK (length(email) <= 254),
    submitted_at timestamptz NOT NULL DEFAULT now(),
    CHECK (name IS NOT NULL OR phone IS NOT NULL OR email IS NOT NULL)
);

CREATE TABLE marketing_consents (
    id        text PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    client_mac macaddr NOT NULL,
    -- The grant it was given with, while that grant exists. SET NULL, not CASCADE:
    -- retention removes grants after ninety days and a consent outlives that.
    grant_id  text REFERENCES portal_grants(id) ON DELETE SET NULL,
    name      text CHECK (length(name)  <= 120),
    phone     text CHECK (length(phone) <= 32),
    email     text CHECK (length(email) <= 254),
    lang      text NOT NULL CHECK (lang IN ('en', 'vi')),
    consent_version text NOT NULL,
    consented_at timestamptz NOT NULL DEFAULT now(),
    withdrawn_at timestamptz,
    withdrawn_by text CHECK (withdrawn_by IN ('guest', 'operator')),

    FOREIGN KEY (tenant_id, site_id, consent_version)
        REFERENCES marketing_consent_texts (tenant_id, site_id, version),
    CHECK ((withdrawn_at IS NULL) = (withdrawn_by IS NULL)),
    CHECK (withdrawn_at IS NULL OR (name IS NULL AND phone IS NULL AND email IS NULL))
);

CREATE INDEX marketing_consents_by_site ON marketing_consents (site_id, consented_at DESC, id);
CREATE INDEX marketing_consents_by_mac  ON marketing_consents (site_id, client_mac) WHERE withdrawn_at IS NULL;
CREATE INDEX marketing_consents_withdrawn ON marketing_consents (site_id, withdrawn_at) WHERE withdrawn_at IS NOT NULL;
CREATE INDEX marketing_consents_by_grant ON marketing_consents (grant_id) WHERE grant_id IS NOT NULL;

ALTER TABLE guest_data_settings     ENABLE ROW LEVEL SECURITY;
ALTER TABLE marketing_consent_texts ENABLE ROW LEVEL SECURITY;
ALTER TABLE guest_details           ENABLE ROW LEVEL SECURITY;
ALTER TABLE marketing_consents      ENABLE ROW LEVEL SECURITY;
ALTER TABLE guest_data_settings     FORCE ROW LEVEL SECURITY;
ALTER TABLE marketing_consent_texts FORCE ROW LEVEL SECURITY;
ALTER TABLE guest_details           FORCE ROW LEVEL SECURITY;
ALTER TABLE marketing_consents      FORCE ROW LEVEL SECURITY;

CREATE POLICY guest_data_settings_isolation ON guest_data_settings
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY marketing_consent_texts_isolation ON marketing_consent_texts
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY guest_details_isolation ON guest_details
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY marketing_consents_isolation ON marketing_consents
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE ON guest_data_settings, guest_details, marketing_consents TO foyer_app;
-- A published text is never rewritten or removed by the application: a consent
-- names it, and a text that changed under a consent would make the record say
-- something the guest never read. It goes only with its site.
GRANT SELECT, INSERT ON marketing_consent_texts TO foyer_app;

-- +goose Down

DROP TABLE marketing_consents;
DROP TABLE guest_details;
DROP TABLE marketing_consent_texts;
DROP TABLE guest_data_settings;
