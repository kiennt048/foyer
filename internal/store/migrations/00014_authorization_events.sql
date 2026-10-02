-- What happened when the network was asked to let a guest on.
--
-- Nothing else records this. `internal/portal` invariant 4 requires the `303` to be
-- flushed **before** the controller is asked, so a controller that refuses does so
-- after the guest has already been told they are connected. There is no response
-- left to tell them with, no `radacct` row will ever exist for a device that never
-- got on, and the audit log cannot hold it -- `AuditEntry` requires an `actorId`,
-- and the actor here is a guest with a voucher.
--
-- So the failure gets a table of its own, and the two readers of it are written
-- **together**: `authorization_events` is what an operator finds through
-- `listAuthAttempts` (`source: driver`, `kind: authorize-failed`), and
-- `portal_grants.delivery` is what the guest's own status read renders. An
-- implementation able to write one without the other could show the console
-- `authorize-failed` beside a portal showing a green tick, which the port's author
-- judged worse than the silence it replaces. `GrantRepo.RecordAuthorization` does
-- both in one transaction; this migration is what makes that possible.
--
-- It is a **Foyer-owned table and not an extension of `radpostauth`**. That column
-- set is stock FreeRADIUS's, kept verbatim as the project's exit hatch, and
-- `foyer_app` holds SELECT on it and nothing else. Nothing here touches it: the two
-- streams are unioned at read time, which is what `AuthAttempt.source` has always
-- said happens.

-- +goose Up

-- ---------------------------------------------------------------------------
-- portal_grants.delivery -- the guest's half of the same fact.
-- ---------------------------------------------------------------------------
--
-- Stored rather than derived, on the same rule as `revoked_at`: expiry is the clock
-- passing a number and needs nothing to happen for it to be true, but a call was
-- made and it returned, and there is nothing about *now* from which that could be
-- recomputed. Asking the controller again to find out would be a second
-- authorization attempt disguised as a read.
--
-- **Nullable, and NULL is the zero value on purpose.** It covers two situations the
-- portal cannot tell apart: the driver call has not returned yet -- which is what a
-- *successful* redemption looks like for the length of one controller round trip,
-- because the redirect is flushed first -- and a deployment whose `Authorizations`
-- port is not wired. Both read `connected`, because *nothing has told us it failed*
-- is not *it failed*.
ALTER TABLE portal_grants
    ADD COLUMN delivery text CHECK (delivery IN ('open', 'failed'));

COMMENT ON COLUMN portal_grants.delivery IS
    'Whether the network was actually opened for this grant. NULL means no answer '
    'has arrived yet, which is indistinguishable from a deployment with no '
    'Authorizations port wired, and both render as connected. Written in the same '
    'transaction as the authorization_events row, never on its own.';

-- ---------------------------------------------------------------------------
-- authorization_events -- the operator's half.
-- ---------------------------------------------------------------------------
--
-- The columns are `AuthAttempt`'s, plus the two that make an `authorize-failed` row
-- actionable: `failure` says which of two people has a job to do, and `status`
-- carries what the controller answered with. `401` and `403` are an expired or
-- revoked API key; `404` is usually the wrong vendor site id.
--
-- **No column may hold the controller's own text.** `nas.APIError` embeds the URL
-- it was reached at and up to 512 bytes of response body; a URL can carry an API
-- key in its query string and a body is text somebody else wrote, and this row is
-- read months later by a person who was not there (docs/rules/security.md 3). There
-- is no column either could arrive in, which is the same shape `radpostauth`'s
-- generated query uses to make a submitted password unrepresentable rather than
-- merely unwritten.
CREATE TABLE authorization_events (
    -- bigserial rather than a generated id, because this stream is read newest
    -- first and paged on the same descending-key cursor `radpostauth` uses. The
    -- two streams share one id namespace in the contract's `AuthAttempt.id`, so
    -- the adapter prefixes this one -- see authorizationEventID.
    id        bigserial PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                   DEFAULT foyer_current_tenant(),
    site_id   text NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    -- The grant this attempt was for. NOT NULL: an authorization with no grant
    -- behind it is not a thing that can happen -- the grant is written first, and
    -- it is what the driver is asked to honour.
    grant_id  text NOT NULL REFERENCES portal_grants(id) ON DELETE CASCADE,
    nas_id    text REFERENCES radius_clients(id) ON DELETE SET NULL,

    client_mac macaddr NOT NULL,
    ap_mac     macaddr,

    method     text NOT NULL CHECK (method IN ('voucher', 'account', 'click-through')),
    -- What was presented, never how. `username` is the identity offered and there
    -- is deliberately no column a credential could land in.
    voucher_id text REFERENCES vouchers(id) ON DELETE SET NULL,
    username   text,

    at       timestamptz NOT NULL,
    delivery text NOT NULL CHECK (delivery IN ('open', 'failed')),
    -- How many times the controller was asked, including the one that succeeded or
    -- the last one that failed. Recorded rather than implied: a retry nobody can
    -- see is a second way to be wrong, because an operator reading *the network was
    -- opened* would have no way to tell a healthy controller from one that missed
    -- the first ask.
    attempts integer NOT NULL CHECK (attempts >= 1),

    failure text CHECK (failure IN ('unreachable', 'refused')),
    status  integer CHECK (status IS NULL OR (status >= 100 AND status < 600)),

    -- An outcome and its explanation cannot disagree. A `failed` row with no
    -- failure kind is a row an operator can do nothing with, and an `open` row
    -- carrying one is a contradiction the console would have to pick a side of.
    CONSTRAINT authorization_events_failure_matches_delivery
        CHECK ((delivery = 'failed') = (failure IS NOT NULL)),
    -- A status is something that was *answered*. Nothing answered an unreachable
    -- controller, so a status beside `unreachable` is a number invented from a
    -- transport error.
    CONSTRAINT authorization_events_status_needs_an_answer
        CHECK (status IS NULL OR failure = 'refused')
);

-- Newest first, per site: the shape of every read this table has.
CREATE INDEX authorization_events_by_site ON authorization_events (site_id, id DESC);

-- The MAC is the only identifier an operator has when a guest is standing in front
-- of them, and it is what the listing's search matches on.
CREATE INDEX authorization_events_by_mac ON authorization_events (site_id, client_mac);

-- A grant's own history, for the read that answers "was this one delivered?".
CREATE INDEX authorization_events_by_grant ON authorization_events (grant_id);

ALTER TABLE authorization_events ENABLE ROW LEVEL SECURITY;

CREATE POLICY authorization_events_isolation ON authorization_events
    USING (tenant_id = foyer_current_tenant())
    WITH CHECK (tenant_id = foyer_current_tenant());

-- SELECT and INSERT, and deliberately neither UPDATE nor DELETE. An event is what
-- happened; there is no correcting it later, and retention is a sweep a separate
-- role runs rather than something the application does row by row.
GRANT SELECT, INSERT ON authorization_events TO foyer_app;

-- **The sequence grant is not boilerplate.** docs/rules/database.md records the case where
-- granting INSERT on a bigserial table left a suite green because `nextval()` still
-- needed USAGE the role did not have, and PostgreSQL answered 42501 either way.
-- Without this line the INSERT above is refused with a message naming the
-- *sequence*, and the writer this whole migration exists for silently records
-- nothing.
GRANT USAGE ON SEQUENCE authorization_events_id_seq TO foyer_app;

-- +goose Down

DROP TABLE authorization_events;
ALTER TABLE portal_grants DROP COLUMN delivery;
