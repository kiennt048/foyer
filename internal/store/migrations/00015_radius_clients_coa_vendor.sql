-- Which member of the CoA family a `driver = 'coa'` client is.
--
-- `radius_clients.driver` has one value, `coa`, for two vendors. They are two
-- different implementations in `internal/nas` because they identify a session
-- differently: Cisco IOS-XE by a `Cisco-AVPair audit-session-id` VSA, Aruba by
-- `Calling-Station-Id` in its own spelling. RFC 5176 §2.3 makes a NAS match on every
-- session-identification attribute in the request, so an identifier in the wrong
-- spelling matches nothing -- which a careful NAS answers with `Error-Cause 503` and
-- a careless one answers by ignoring the request and ACKing it.
--
-- That last case is why this column exists rather than a default. An ACK with no
-- effect is the one CoA failure `lab/` cannot produce: a fake NAS that ignores what
-- it cannot match answers success, so a wrong guess is green everywhere and wrong on
-- the only hardware nobody here has.
--
-- ── nullable, and NULL is a real answer ─────────────────────────────────────
--
-- NULL is *nobody has said*. It is kept representable on purpose: the alternative
-- shape, splitting `driver` into `cisco` and `aruba`, would need this migration to
-- rewrite every existing `coa` row to one vendor or the other with nothing to decide
-- it on -- which is exactly the guess the column exists to prevent, taken once, in
-- SQL, on somebody else's estate.
--
-- So a `coa` row with no vendor is refused where the driver is built
-- (`apistore.ErrCoAVendorUnknown`, which names the row) and refused again at
-- registration (`createNas` returns `required` on `coaVendor`). Two gates, because
-- the second one cannot reach a row that predates it.
--
-- It is **not** a third inert switch beside `nas.require_message_authenticator` and
-- `nas.limit_proxy_state` (`docs/rules/security.md` §3). Those two are read by
-- nothing. This one has a writer -- `NasCreate.coaVendor` -- and a reader --
-- `VendorDrivers.ForNAS` -- landing in the same change; without both it would have
-- been neither added nor useful.

-- +goose Up

ALTER TABLE radius_clients
    ADD COLUMN coa_vendor text
        CHECK (coa_vendor IS NULL OR coa_vendor IN ('cisco', 'aruba'));

COMMENT ON COLUMN radius_clients.coa_vendor IS
    'Which CoA dialect a driver=coa client speaks: cisco or aruba. NULL means '
    'nobody has said, and such a client is refused a driver by name rather than '
    'being given a guessed one -- the wrong dialect identifies no session and a '
    'careless NAS answers that with an ACK and no effect. Read only where driver '
    'is coa, as coa_port is read only where CoA is spoken at all.';

-- No policy is added here, and that is not an omission. `radius_clients` carries
-- `radius_clients_isolation` from migration 00004 and a policy is a rule about
-- *rows*; a column added to a table under RLS is already covered by it. A second
-- policy on the same table would be OR'ed with the first (PostgreSQL policies are
-- permissive by default), so it could only widen access, never narrow it.
--
-- `foyer_app` already holds SELECT, INSERT, UPDATE, DELETE on the whole table from
-- 00004, so the column needs no grant either.

-- +goose Down

ALTER TABLE radius_clients DROP COLUMN coa_vendor;
