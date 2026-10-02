-- A consent remembers the account it was given under, and every retention period
-- has a floor the database itself holds.
--
-- ── an account's consents outlived the link that found them ─────────────────
--
-- The account eraser found a guest account's marketing consents through the grants
-- the account signed in with (`grant_id IN (SELECT id FROM portal_grants WHERE
-- account_id = …)`). `marketing_consents.grant_id` is ON DELETE SET NULL (00023), on
-- purpose: the portalGrants period is ninety days and a consent outlives it. So after
-- the first sweep past a grant's end the consent no longer pointed at anything that
-- named the account, and erasing the account left the consent -- e-mail, phone and
-- MAC -- behind while the receipt said 0. Reproduced by
-- `TestEraseByAccountRemovesConsentsWhoseGrantAlreadyAgedOut`.
--
-- The fix is the one PacketFence's `person` table makes for the same reason: the
-- record names the person directly rather than through a session that ages out.
-- `account_id` is written when the consent is recorded (store.RecordConsent, from the
-- grant it was given with) and backfilled here for every consent whose grant still
-- exists. SET NULL rather than CASCADE: an account removed by its own retention
-- period (guestAccounts) is not an erasure, and a marketing consent is kept for its
-- own period; an *erasure* removes the consent first (Order 15 < 90).
--
-- ── the floor lived only in Go ───────────────────────────────────────────────
--
-- `retention_policies.days` was checked against 0..3650 and nothing else, and the
-- per-category minimums (audit 30 days, …) were `store.RetentionCategory.MinDays`
-- only. A statement run as `foyer_app` -- an injection, a console bug -- could set
-- the audit period to one day, and the next sweep, running as foyer_retention, would
-- delete two years of accountability on the application's say-so. The CHECK below
-- is the same numbers held where the application's role cannot move them;
-- `TestTheDatabaseHoldsEveryRetentionFloor` fails for a category whose Go minimum
-- and this constraint disagree, and `SweepRetention` re-applies the minimum itself
-- in case a future migration drops it.
--
-- A row already below its floor (it cannot have got there through the API, which
-- refused it) is raised to the floor before the constraint is added, so the
-- migration cannot fail on the very tampering it closes.

-- +goose Up

ALTER TABLE marketing_consents
    ADD COLUMN account_id text REFERENCES accounts(id) ON DELETE SET NULL;
CREATE INDEX marketing_consents_by_account ON marketing_consents (account_id) WHERE account_id IS NOT NULL;

COMMENT ON COLUMN marketing_consents.account_id IS
    'The guest account the consent was given under, when it was. Written with the '
    'consent (from its grant) so an erasure by account still finds it after the grant '
    'has aged out -- grant_id is SET NULL by then (00051).';

-- Across every tenant, so the owner steps outside the forced policies for these two
-- statements: with FORCE on and no `foyer.tenant_id`, both would match nothing and
-- report success (00040 has the same note).
ALTER TABLE marketing_consents NO FORCE ROW LEVEL SECURITY;
ALTER TABLE portal_grants      NO FORCE ROW LEVEL SECURITY;
UPDATE marketing_consents c SET account_id = g.account_id
FROM portal_grants g
WHERE g.id = c.grant_id AND g.account_id IS NOT NULL AND c.account_id IS NULL;
ALTER TABLE portal_grants      FORCE ROW LEVEL SECURITY;
ALTER TABLE marketing_consents FORCE ROW LEVEL SECURITY;

-- The floors. The same figures as store.RetentionCategories' MinDays; a category the
-- code registers later and this list does not name falls to 0 here, which
-- `TestTheDatabaseHoldsEveryRetentionFloor` turns red until it is added.
-- +goose StatementBegin
CREATE FUNCTION foyer_retention_floor(category text) RETURNS integer
LANGUAGE sql IMMUTABLE PARALLEL SAFE
SET search_path = pg_catalog
AS $$
    SELECT CASE category
        WHEN 'accounting'        THEN 1
        WHEN 'authAttempts'      THEN 1
        WHEN 'portalGrants'      THEN 1
        WHEN 'audit'             THEN 30
        WHEN 'marketingConsents' THEN 30
        WHEN 'guestAccounts'     THEN 0
        ELSE 0
    END
$$;
-- +goose StatementEnd

ALTER TABLE retention_policies NO FORCE ROW LEVEL SECURITY;
UPDATE retention_policies SET days = foyer_retention_floor(category)
WHERE days < foyer_retention_floor(category);
ALTER TABLE retention_policies FORCE ROW LEVEL SECURITY;

ALTER TABLE retention_policies
    ADD CONSTRAINT retention_policies_floor CHECK (days >= foyer_retention_floor(category));

-- +goose Down

ALTER TABLE retention_policies DROP CONSTRAINT retention_policies_floor;
DROP FUNCTION foyer_retention_floor(text);
DROP INDEX marketing_consents_by_account;
ALTER TABLE marketing_consents DROP COLUMN account_id;
