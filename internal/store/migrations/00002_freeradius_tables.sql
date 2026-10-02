-- The three tables that belong to the AAA data plane.
--
-- `radacct` and `radpostauth` carry the **stock FreeRADIUS 3.2.x column set,
-- verbatim**. That is not deference: it is the project's exit hatch. If Foyer
-- disappears the accounting history stays readable by a stock FreeRADIUS
-- installation, and a single added column would end that. Indexes, partitioning and
-- retention are ours; the column set is not.
--
-- Two consequences follow and both are load-bearing:
--
--   * **Neither table carries `tenant_id`,** because the stock schema has no such
--     column. Tenant scoping on the read path is therefore derived, by joining
--     `radacct.nasipaddress` to `radius_clients`, which does carry it and does have
--     a row-level policy. `internal/store/CONTRACT.md` states this; the accounting
--     repository is the only place allowed to read these tables.
--   * **Neither table carries a row-level policy,** for the same reason. The
--     protection here is a GRANT instead, which is stronger: RLS filters rows, a
--     missing INSERT privilege refuses the statement.
--
-- The `pass` column exists because the stock schema has it. Nothing writes it. The
-- generated post-auth query omits it, there is a snapshot test on that generated
-- config, and `foyer_radius` is granted INSERT on the table rather than on a column
-- list only because a column-level grant would silently break the day upstream adds
-- a column. See docs/rules/security.md §1.

-- +goose Up

CREATE TABLE radacct (
    radacctid           bigserial PRIMARY KEY,
    acctsessionid       text NOT NULL,
    acctuniqueid        text NOT NULL UNIQUE,
    username            text,
    groupname           text,
    realm               text,
    nasipaddress        inet NOT NULL,
    nasportid           text,
    nasporttype         text,
    acctstarttime       timestamp with time zone,
    acctupdatetime      timestamp with time zone,
    acctstoptime        timestamp with time zone,
    acctinterval        bigint,
    acctsessiontime     bigint,
    acctauthentic       text,
    connectinfo_start   text,
    connectinfo_stop    text,
    acctinputoctets     bigint,
    acctoutputoctets    bigint,
    calledstationid     text,
    callingstationid    text,
    acctterminatecause  text,
    servicetype         text,
    framedprotocol      text,
    framedipaddress     inet,
    framedipv6address   inet,
    framedipv6prefix    inet,
    framedinterfaceid   text,
    delegatedipv6prefix inet,
    class               text
);

CREATE INDEX radacct_active_session_idx  ON radacct (acctuniqueid) WHERE acctstoptime IS NULL;
CREATE INDEX radacct_bulk_close          ON radacct (nasipaddress, acctstarttime) WHERE acctstoptime IS NULL;
CREATE INDEX radacct_callingstationid_idx ON radacct (callingstationid);
CREATE INDEX radacct_nasipaddress_idx    ON radacct (nasipaddress);
-- The console's default order, and the reaper's scan. Without it both sequential
-- scan a table that only ever grows.
CREATE INDEX radacct_starttime_idx       ON radacct (acctstarttime DESC);

CREATE TABLE radpostauth (
    id               bigserial PRIMARY KEY,
    username         text NOT NULL,
    pass             text,
    reply            text NOT NULL,
    calledstationid  text,
    callingstationid text,
    authdate         timestamp with time zone NOT NULL DEFAULT now(),
    class            text
);

CREATE INDEX radpostauth_callingstationid_idx ON radpostauth (callingstationid);
CREATE INDEX radpostauth_authdate_idx         ON radpostauth (authdate DESC);

-- FreeRADIUS's SQL clients table, read with `read_clients = yes`.
--
-- This is the one place in the deployment where a RADIUS shared secret exists in
-- cleartext, and it exists there because FreeRADIUS validates a Message-Authenticator
-- against the secret itself and has no way to ask Foyer for it. The system of record
-- is `shared_secrets` (migration 00004), where the value is envelope-encrypted under
-- a key that is not in the database; this table is a projection generated from it.
-- Saying so is the point -- an encrypted column beside a cleartext copy nobody
-- mentions is worse than an honest cleartext one.
--
-- `foyer_radius` may only SELECT here. Foyer writes it, FreeRADIUS reads it, and
-- adding an access point is therefore a plain database write rather than a restart
-- that drops every in-flight EAP conversation.
CREATE TABLE nas (
    id                            serial PRIMARY KEY,
    nasname                       text NOT NULL UNIQUE,
    shortname                     text NOT NULL,
    type                          text NOT NULL DEFAULT 'other',
    ports                         integer,
    secret                        text NOT NULL,
    server                        text,
    community                     text,
    description                   text,
    require_message_authenticator boolean NOT NULL DEFAULT true,
    limit_proxy_state             boolean NOT NULL DEFAULT true
);

-- ---------------------------------------------------------------------------
-- The grants that make "Go never writes radacct or radpostauth" a fact.
-- ---------------------------------------------------------------------------

GRANT SELECT, INSERT, UPDATE ON radacct     TO foyer_radius;
GRANT SELECT, INSERT         ON radpostauth TO foyer_radius;
GRANT SELECT                 ON nas         TO foyer_radius;
GRANT USAGE ON SEQUENCE radacct_radacctid_seq, radpostauth_id_seq TO foyer_radius;

-- The application role. SELECT, and deliberately nothing else. There is a test that
-- attempts an INSERT through this role and requires PostgreSQL to refuse it.
GRANT SELECT ON radacct, radpostauth TO foyer_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON nas TO foyer_app;
GRANT USAGE ON SEQUENCE nas_id_seq TO foyer_app;

-- The stale-session reaper, and the narrowest grant that lets it do its one job.
--
-- A session whose NAS rebooted never receives an Acct-Stop and stays open forever if
-- nothing closes it. Closing it means writing exactly these four columns: when it
-- ended, why, how long it ran, and that we touched it. It cannot insert a session,
-- cannot delete one, cannot alter a byte counter, and has no privilege at all on
-- `radpostauth`.
GRANT SELECT ON radacct TO foyer_reaper;
GRANT UPDATE (acctstoptime, acctterminatecause, acctsessiontime, acctupdatetime)
    ON radacct TO foyer_reaper;

-- +goose Down

REVOKE ALL ON radacct, radpostauth, nas FROM foyer_app, foyer_radius, foyer_reaper;
REVOKE ALL ON SEQUENCE radacct_radacctid_seq, radpostauth_id_seq, nas_id_seq
    FROM foyer_app, foyer_radius;
DROP TABLE nas;
DROP TABLE radpostauth;
DROP TABLE radacct;
