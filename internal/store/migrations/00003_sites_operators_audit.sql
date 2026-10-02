-- Sites, the operators who administer them, their console sessions, and the audit
-- log.
--
-- Identifiers are `text` rather than `uuid` throughout because the contract types
-- every id as a string and the fixtures use readable ones (`site-hcm`, `op-linh`).
-- A uuid column would force every one of them through a translation layer that buys
-- nothing.

-- +goose Up

CREATE TABLE sites (
    id          text PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                     DEFAULT foyer_current_tenant(),
    name        text NOT NULL,
    -- `lab` is not decoration. A site marked lab is the one an operator may point at
    -- a fake NAS without wondering whether they have just taken a venue offline.
    environment text NOT NULL DEFAULT 'live' CHECK (environment IN ('live', 'lab')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX sites_tenant_idx ON sites (tenant_id);

CREATE TABLE operators (
    id            text PRIMARY KEY,
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                       DEFAULT foyer_current_tenant(),
    username      text NOT NULL,
    display_name  text,
    -- Argon2id, always. An operator never authenticates over a challenge-response
    -- protocol, so nothing here forces a recoverable verifier. Guest identities that
    -- do are in migration 00005, in their own table, for exactly that reason.
    password_hash text NOT NULL,
    role          text NOT NULL CHECK (role IN ('administrator', 'staff')),
    enabled       boolean NOT NULL DEFAULT true,
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, username)
);

CREATE TABLE operator_sessions (
    -- The token itself is never stored. What is stored is a hash of it, so a
    -- database read does not hand out live console sessions.
    token_hash text PRIMARY KEY,
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                    DEFAULT foyer_current_tenant(),
    operator_id text NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
    -- Paired with the session cookie and required on every state-changing request:
    -- the double-submit half of the CSRF defence.
    csrf_token text NOT NULL,
    user_agent text,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX operator_sessions_operator_idx ON operator_sessions (operator_id);
CREATE INDEX operator_sessions_expiry_idx   ON operator_sessions (expires_at);

-- Append-only, and enforced as such by the grants below rather than by a comment.
--
-- `actor_username` is denormalised on purpose: an audit row must still say who did
-- the thing after that operator has been deleted, and a foreign key to a name would
-- either block the delete or lose the name.
CREATE TABLE audit_log (
    id             text PRIMARY KEY,
    tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                        DEFAULT foyer_current_tenant(),
    at             timestamptz NOT NULL DEFAULT now(),
    actor_id       text NOT NULL,
    actor_username text NOT NULL,
    kind           text NOT NULL,
    params         jsonb NOT NULL DEFAULT '{}'::jsonb,
    site_id        text REFERENCES sites(id) ON DELETE SET NULL,
    subject_kind   text NOT NULL,
    subject_id     text NOT NULL,
    subject_name   text
);

CREATE INDEX audit_log_at_idx      ON audit_log (at DESC);
CREATE INDEX audit_log_site_at_idx ON audit_log (site_id, at DESC);
CREATE INDEX audit_log_kind_idx    ON audit_log (kind);

ALTER TABLE sites             ENABLE ROW LEVEL SECURITY;
ALTER TABLE operators         ENABLE ROW LEVEL SECURITY;
ALTER TABLE operator_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log         ENABLE ROW LEVEL SECURITY;

CREATE POLICY sites_isolation ON sites
    USING (tenant_id = foyer_current_tenant())
    WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY operators_isolation ON operators
    USING (tenant_id = foyer_current_tenant())
    WITH CHECK (tenant_id = foyer_current_tenant());
CREATE POLICY operator_sessions_isolation ON operator_sessions
    USING (tenant_id = foyer_current_tenant())
    WITH CHECK (tenant_id = foyer_current_tenant());

-- Two policies rather than one, because the audit log is readable and insertable and
-- nothing else. UPDATE and DELETE have no policy at all, which is belt to the
-- grant's braces.
CREATE POLICY audit_log_read ON audit_log FOR SELECT
    USING (tenant_id = foyer_current_tenant());
CREATE POLICY audit_log_append ON audit_log FOR INSERT
    WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE ON sites, operators, operator_sessions TO foyer_app;
-- No UPDATE. No DELETE. The audit log's immutability is a privilege, not a promise.
GRANT SELECT, INSERT ON audit_log TO foyer_app;

-- +goose Down

DROP TABLE audit_log;
DROP TABLE operator_sessions;
DROP TABLE operators;
DROP TABLE sites;
