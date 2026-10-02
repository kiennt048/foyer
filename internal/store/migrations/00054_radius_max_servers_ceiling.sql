-- The FreeRADIUS worker count has a ceiling the database itself holds.
--
-- The console accepted up to 256 FreeRADIUS workers, each of which may hold an
-- rlm_sql connection, against PostgreSQL's max_connections = 80. The console's
-- maximum was lowered to api.RadiusMaxServersCeiling (44 = 80 − 3 superuser − 25
-- control plane − 4 maintenance − 4 one-shot, see internal/api/netsettings.go), and
-- apistore.generatorSettings clamps a stored figure, but the column's CHECK stayed
-- `> 0`: a ceiling held only in Go is the shape the retention floors had before
-- 00051 (a floor held only in Go, which SQL injection as foyer_app walks past).
-- This is the SQL half.
--
-- A row already above the ceiling -- saved while the console allowed 256 -- is
-- lowered to it first, which is what the generator has already been doing with it
-- since the clamp landed, so nothing FreeRADIUS runs changes.
--
-- `TestTheDatabaseHoldsTheWorkerCeiling` (internal/apistore) reads the Go constant
-- and proves foyer_app can store it and cannot store one more, so a change to the
-- constant without a migration is red.

-- +goose Up

UPDATE network_settings SET radius_max_servers = 44 WHERE radius_max_servers > 44;
ALTER TABLE network_settings DROP CONSTRAINT IF EXISTS network_settings_radius_max_servers_check;
ALTER TABLE network_settings ADD CONSTRAINT network_settings_radius_max_servers_ceiling
    CHECK (radius_max_servers > 0 AND radius_max_servers <= 44);

-- +goose Down

ALTER TABLE network_settings DROP CONSTRAINT network_settings_radius_max_servers_ceiling;
ALTER TABLE network_settings ADD CONSTRAINT network_settings_radius_max_servers_check
    CHECK (radius_max_servers > 0);
