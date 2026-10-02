-- The stale-session reaper and the retention sweep log in as their own roles.
--
-- ── why, and why now ─────────────────────────────────────────────────────────
--
-- 00019 made the application and FreeRADIUS log in as `foyer_app` and
-- `foyer_radius`, and made the application's login a member of no other role:
-- `store.NewStore` refuses one that is (`ErrOverPrivilegedLogin`), because a login
-- that can SET ROLE into a role with more privilege gives that privilege to every
-- statement in the pool. It left `foyer_reaper` NOLOGIN, with the note "whoever
-- wires the reaper gives it a login the same way".
--
-- 00020 wired it, and added `foyer_retention` — the
-- one role that may DELETE from `radacct` and `radpostauth`. cmd/foyer opens a pool
-- for each: with `FOYER_REAPER_DATABASE_URL` / `FOYER_RETENTION_DATABASE_URL` it
-- logs in as the role, and without them it falls back to SET ROLE on the
-- application's URL — which, after 00019, PostgreSQL refuses. Without a login for
-- each role the two jobs would fail every cycle and `eraseDataSubject` would
-- answer `/problems/internal`.
--
-- ── what this does ───────────────────────────────────────────────────────────
--
-- LOGIN and nothing else, for 00019's reasons: no password here (a password in a
-- migration is a password in the repository), so a role this leaves can log in
-- only once `foyerctl migrate` sets the secret deploy/first-boot.sh generated
-- (FOYER_REAPER_DB_PASSWORD / FOYER_RETENTION_DB_PASSWORD). Neither role gains a
-- privilege: the reaper keeps 00001's grants and retention keeps 00020's SELECT +
-- DELETE, which `store.OpenRetention` re-checks on every connection it opens.

-- +goose Up

ALTER ROLE foyer_reaper    LOGIN;
ALTER ROLE foyer_retention LOGIN;

-- +goose Down

-- LOGIN is deliberately NOT revoked, for 00001's and 00019's reason: role
-- attributes are cluster-level, and a Down in one database cannot know that another
-- database in the same cluster is not logging in as these roles right now. A role
-- with no password still cannot authenticate by password.
SELECT 1;
