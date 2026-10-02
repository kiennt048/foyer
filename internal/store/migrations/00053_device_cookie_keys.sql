-- The console's trusted-device cookie key ring, persisted.
--
-- `internal/api/devicecookie.go` signs a cookie that lets a browser which signed
-- in as an account before spend from its own sign-in bucket, so a stranger
-- emptying the account's shared bucket cannot lock the owner out (OWASP's device
-- cookies). Its key was drawn when the process started, and the
-- consequence was reproducible: a restart or an upgrade untrusted every
-- device at once, and a stranger already flooding the account then held the owner
-- out through the only bucket left. The defence failed at the moment it mattered.
--
-- One row per key. **The key is sealed** (envelope.go): AES-GCM under a data key
-- wrapped by FOYER_KEK, bound to this table, this column and the key's id, so a
-- database read — or a SQL injection as foyer_app — does not yield a key that mints
-- trusted-device cookies. The newest row signs; an older one only verifies, for a
-- cookie's lifetime after its successor arrived, and is then deleted by the same
-- transaction that reads the ring (store.DeviceCookieKeyRepo.Ring). The rotation
-- period is a policy in internal/api (ninety days), not a restart.
--
-- Not personal data: a random key, naming nobody. foyer_radius, foyer_reaper and
-- foyer_retention have no grant.

-- +goose Up

CREATE TABLE device_cookie_keys (
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                    DEFAULT foyer_current_tenant(),
    id         text NOT NULL CHECK (id ~ '^[0-9a-f]{16}$'),
    key_sealed bytea NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX device_cookie_keys_by_age ON device_cookie_keys (tenant_id, created_at DESC);

ALTER TABLE device_cookie_keys ENABLE ROW LEVEL SECURITY;
-- Forced, as 00019 forces every table with a policy (`TestEveryRowLevelPolicyIsForced`).
ALTER TABLE device_cookie_keys FORCE ROW LEVEL SECURITY;

CREATE POLICY device_cookie_keys_isolation ON device_cookie_keys
    USING (tenant_id = foyer_current_tenant()) WITH CHECK (tenant_id = foyer_current_tenant());

-- No UPDATE: a key is never changed, only minted and, once retired long enough,
-- deleted.
GRANT SELECT, INSERT, DELETE ON device_cookie_keys TO foyer_app;

COMMENT ON TABLE device_cookie_keys IS
    'The console trusted-device cookie key ring. key_sealed is enveloped under FOYER_KEK '
    '(bound to device_cookie_keys/key_sealed/<id>). Newest signs; older rows verify for '
    'the cookie lifetime after their successor, then are deleted.';

-- +goose Down

DROP TABLE device_cookie_keys;
