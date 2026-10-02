-- What a venue says to its guests: a brand colour, a headline and content blocks.
--
-- The contract's `PortalDesign` (`web/contract/openapi.yaml`). The goal is a page a
-- non-technical person can edit; `/portal` was a settings form with no content in
-- it at all.
--
-- ── one jsonb column, not a blocks table ─────────────────────────────────────
--
-- A design is written whole and read whole. `updatePortalSettings` replaces it as
-- one value -- partial updates of a block list are how two console tabs lose each
-- other's edits -- and every guest document reads all of it. Twelve blocks at most
-- (the contract's `maxItems`), each a few hundred bytes. Nothing ever queries one
-- block, joins on one, or needs a constraint across them that the API does not
-- already enforce before the write, so a child table would buy ordering columns and
-- a delete-and-reinsert on every save and nothing else. `terms` sits beside it in
-- the same shape for the same reason.
--
-- **The shape is the API's to enforce, not this column's.** The request validator
-- holds the schema (lengths, enums, the accent pattern) and `updatePortalSettings`
-- holds the rules beyond it (duplicate ids, links before sign-in, dates). A CHECK
-- here restating either would be `docs/rules/database.md`'s restated
-- constraint -- the second copy that drifts the day the contract changes.
--
-- ── the default is the empty design, not NULL ────────────────────────────────
--
-- `PortalSettings.design` is required on the wire. Every existing row gets the
-- design that renders exactly the page Foyer rendered before designs existed:
-- no accent (Foyer's teal), no headline (the catalogue's "Get online"), no blocks.

-- +goose Up
ALTER TABLE portal_settings
    ADD COLUMN design jsonb NOT NULL
        DEFAULT '{"accent":null,"headline":{},"blocks":[]}'::jsonb;

COMMENT ON COLUMN portal_settings.design IS
    'The contract''s PortalDesign, stored verbatim: accent, per-language headline, '
    'content blocks. Validated by updatePortalSettings before it is written; narrowed '
    '(hidden and out-of-date blocks dropped) before any guest reads it.';

-- +goose Down
ALTER TABLE portal_settings DROP COLUMN design;
