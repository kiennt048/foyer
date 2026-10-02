-- The third noun on a portal session: which controller manages the access point.
--
-- `portal_sessions` already carried the Foyer site, the RADIUS client and the
-- vendor's site id. It did not carry the controller, so every call across the
-- driver seam resolved it again from the access-point MAC -- `VerifyPresence`
-- once, `AuthorizeGuest` twice, and `HardQuota` once more.
--
-- The repeated query was the cheap half. The expensive half is that a device
-- confirmed present against one controller could be authorized against another:
-- `access_points.controller_id` is a live value, discovery rewrites it, and
-- docs/rules/security.md §4 makes presence the precondition for a grant. A precondition
-- checked against one thing and acted on against another is not a precondition.
--
-- So the placement is resolved once, at the front door, and stored with the rest
-- of it. `docs/architecture.md` is the model this completes: controller, RADIUS
-- client, access point -- three nouns, and this row was carrying two of them.

-- +goose Up

ALTER TABLE portal_sessions
    ADD COLUMN controller_id text REFERENCES controllers(id) ON DELETE SET NULL;

COMMENT ON COLUMN portal_sessions.controller_id IS
    'The controller that managed this access point when the session was minted. '
    'ON DELETE SET NULL rather than CASCADE: deleting a controller must not delete '
    'the sessions that ran through it, and a session whose controller is gone '
    'refuses at the driver seam rather than silently choosing another one.';

-- +goose Down

ALTER TABLE portal_sessions DROP COLUMN controller_id;
