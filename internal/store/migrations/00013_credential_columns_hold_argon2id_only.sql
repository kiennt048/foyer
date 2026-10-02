-- "Argon2id, always" becomes something the database refuses rather than
-- something a comment asks for.
--
-- `docs/rules/security.md` §1 is the most consequential constraint in this
-- codebase, and its first bullet is that admin accounts and portal/guest accounts
-- are argon2id. Until this migration that was a comment on the column and a habit
-- in `internal/apistore`; nothing stopped a future caller writing a cleartext
-- password, an NT hash, or an SSHA digest into `password_hash`.
--
-- **Why a CHECK and not a code path.** `docs/rules/database.md`: a validator
-- that re-implements a rule the database could own is a second source of truth
-- that drifts silently. The constraint belongs where the value lands, and a test
-- for it attempts the write and lets PostgreSQL refuse it.
--
-- ── what this is actually protecting against, as measured ───────────────────
--
-- `docs/rules/testing.md` names "no argon2-hashed identity is ever reachable
-- by MSCHAPv2" as a security test. Measured against FreeRADIUS 3.2.10 with a real
-- supplicant (`docs/protocol-reality.md`), the shape of the risk is not
-- what it sounds like:
--
--   * An identity whose only verifier is a one-way hash IS unreachable by
--     MSCHAPv2, and the server says so — "FAILED: No NT-Password. Cannot perform
--     authentication". That half holds by physics.
--   * The way it stops holding is a **cleartext** value in the credential column.
--     rlm_mschap derives the NT hash from `Cleartext-Password` on the fly, so one
--     row containing a password rather than a verifier makes that identity
--     MSCHAPv2-authenticable — with no schema change, no opt-in, and no row in
--     `account_nt_hashes` for the console to render `argon2id+nt` from.
--   * And worse than cleartext: `rlm_pap` rewrites an **unrecognised**
--     `Password-With-Header` value to `Cleartext-Password`, so a stored hash in a
--     scheme FreeRADIUS does not know becomes the password itself. Proven with an
--     eapol_test run that authenticated by sending the stored argon2id verifier
--     as the password.
--
-- So the column that must be constrained is the ordinary one, and the value that
-- must be impossible in it is anything that is not an argon2id verifier.
--
-- `operators` gets the same constraint. An admin password has even less business
-- being anything else, and §1 names both tables in one sentence.
--
-- `account_nt_hashes.nt_hash` is deliberately untouched: it is `bytea`,
-- envelope-encrypted, opted into explicitly, and it is the one place
-- password-equivalent material is allowed to exist.

-- +goose Up

ALTER TABLE accounts
    ADD CONSTRAINT accounts_password_hash_is_argon2id
    CHECK (password_hash LIKE '$argon2id$%');

COMMENT ON CONSTRAINT accounts_password_hash_is_argon2id ON accounts IS
    'security.md 1: portal and guest accounts are argon2id, always. A cleartext '
    'password here would make the identity MSCHAPv2-authenticable without any '
    'opt-in, because rlm_mschap derives the NT hash from Cleartext-Password; a '
    'hash in a scheme FreeRADIUS does not recognise is worse, because rlm_pap '
    'rewrites an unknown Password-With-Header to Cleartext-Password and the hash '
    'becomes the password. Measured on FreeRADIUS 3.2.10.';

ALTER TABLE operators
    ADD CONSTRAINT operators_password_hash_is_argon2id
    CHECK (password_hash LIKE '$argon2id$%');

COMMENT ON CONSTRAINT operators_password_hash_is_argon2id ON operators IS
    'security.md 1: admin accounts are argon2id, always, and they never need '
    'MSCHAPv2. Same reasoning as accounts_password_hash_is_argon2id.';

-- +goose Down

ALTER TABLE accounts  DROP CONSTRAINT accounts_password_hash_is_argon2id;
ALTER TABLE operators DROP CONSTRAINT operators_password_hash_is_argon2id;
