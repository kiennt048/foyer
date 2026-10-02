-- Range indexes for the `reports` operations.
--
-- An export or a trend is a range of one venue's rows by time. `radacct` already
-- has `radacct_starttime_idx` from 00002, which is the one the session export and
-- the accounting trends need. The two tables Foyer owns did not:
--
--   - `portal_grants` is indexed `(site_id, client_mac, granted_at)`, which answers
--     *this device's grants* and not *this venue's grants this month* — a range on
--     the third column of that index is a walk of every MAC. Every guest who comes
--     through the portal adds a row, so this is the other table that only grows.
--   - `vouchers` is indexed `(site_id, state)` and `(batch_id)`. The voucher export
--     is *created or first used in the range*, and the trend counts first use per
--     bucket; neither column was indexed.
--
-- Indexes only. No column, no constraint, no grant changes — the application's role
-- reads these tables already. `radacct`'s column set stays verbatim, and no index
-- is added to it here: 00002's starttime index is the one the range needs.

-- The one partial index is partial **for size, not for correctness**
-- (`docs/rules/database.md`, "say what a partial index is for"): an unused
-- voucher has no first use and can never match a range on it, and a printed batch
-- of five hundred is five hundred rows the index would carry for nothing.

-- +goose Up
CREATE INDEX portal_grants_site_granted_idx ON portal_grants (site_id, granted_at);
CREATE INDEX vouchers_site_created_idx      ON vouchers (site_id, created_at);
CREATE INDEX vouchers_site_first_used_idx   ON vouchers (site_id, first_used_at)
    WHERE first_used_at IS NOT NULL;

-- +goose Down
DROP INDEX vouchers_site_first_used_idx;
DROP INDEX vouchers_site_created_idx;
DROP INDEX portal_grants_site_granted_idx;
