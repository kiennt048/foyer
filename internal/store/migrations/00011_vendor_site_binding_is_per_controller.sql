-- +goose Up
-- The binding was globally exclusive, and that was broader than the ambiguity it
-- named.
--
-- 00004 wrote `UNIQUE (bound_site_id)` with the comment: "a site whose vouchers are
-- authorised by two controllers has no answer to 'which one revokes this grant'."
-- The premise is false. Every resolution path goes through the **device**, not the
-- site: `driverFor(controllerID, …)`, `vendorSiteFrom(controller, siteID)`, and
-- `Placement(apMAC)` through `access_points.controller_id`. A grant carries
-- `ap_mac`, so "which controller revokes this" always has an answer.
--
-- What the old index actually forbade was a **mixed-vendor site**, which is a real
-- deployment rather than a mistake. Measured on the fixture world: with
-- `nas-omada-1` a site-hcm device on ctl-omada, the exclusive index makes
-- `vendorSiteFrom` return "" and **the site cannot authorize guests behind its
-- second vendor's radio at all.**
--
-- The genuine ambiguity is narrower: two vendor sites **of one controller** bound to
-- one Foyer site. Given a device, that has no answer. This is what remains forbidden.
--
-- The narrow reading has now been the true one seven times on this project.
DROP INDEX IF EXISTS controller_vendor_sites_bound_idx;

CREATE UNIQUE INDEX controller_vendor_sites_bound_idx
    ON controller_vendor_sites (controller_id, bound_site_id)
    WHERE bound_site_id IS NOT NULL;

-- +goose Down
-- Restores the exclusive binding. Any deployment with a mixed-vendor site will fail
-- this, which is the correct outcome: the data is legitimate and the constraint is
-- the thing that was wrong.
DROP INDEX IF EXISTS controller_vendor_sites_bound_idx;

CREATE UNIQUE INDEX controller_vendor_sites_bound_idx
    ON controller_vendor_sites (bound_site_id) WHERE bound_site_id IS NOT NULL;
