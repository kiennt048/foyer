-- When a site's calendar last changed, as its own fact.
--
-- `getSiteTimeZone.updatedAt` answered `sites.updated_at`, which is the site row's
-- last change of ANY kind -- a rename moved it, and it was not the time the zone
-- changed, which is what the contract means and what `web/mock` answers. So the
-- zone gets its own timestamp.
--
-- NULL is "never changed by an operator": every existing site was given
-- Asia/Ho_Chi_Minh by migration 00033, and that default is nobody's decision with a
-- time on it. The mock answers null for the same case. A write that stores the zone
-- a site already has is not a change and leaves it alone (store.SiteRepo.SetTimeZone).

-- +goose Up
ALTER TABLE sites ADD COLUMN time_zone_changed_at timestamptz;

-- +goose Down
ALTER TABLE sites DROP COLUMN time_zone_changed_at;
