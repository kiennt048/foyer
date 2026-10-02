-- The operator is still using the credential this install shipped with.
--
-- The decision this column serves: Foyer ships `admin` / `admin` rather than
-- generating a password and printing it once, so that a first-time operator can
-- reach the console without hunting a value out of a terminal, and that the first
-- thing the product does is make them change it. That is Grafana's shape.
--
-- **It contradicts `docs/rules/security.md` §7**, and
-- the contradiction is recorded here rather than smoothed over: §7 says no static
-- credential anywhere and refuse to start on a known default, and the whole reason
-- it says that is that default credentials in an appliance are how this class of
-- software gets taken in bulk. The newer decision stands; this column is
-- what makes the mitigation it came with real.
--
-- ── why a column rather than a comparison ────────────────────────────────────
--
-- The obvious alternative is *"force a change while the password verifies against
-- `admin`"*, which is what Grafana does and needs no schema at all. It is refused
-- here for one reason: `operators.password_hash` is Argon2id, so answering that
-- question means running a KDF against a candidate on every request that consults
-- the flag. A boolean is read with the row.
--
-- The second reason is that the two are not the same proposition. An operator who
-- deliberately *chooses* `admin` after being made to change is a different state
-- from one who never changed, and a comparison cannot tell them apart. The change
-- operation refuses known defaults outright, so the state is unreachable — but the
-- column does not depend on that refusal staying in place.
--
-- ── it can only ever be turned off ───────────────────────────────────────────
--
-- Nothing in the API sets it. `foyerctl create-operator --must-change-password` is
-- the only writer, and `changeOwnPassword` is the only clearer. A *server* deciding
-- an operator's password must change is password-expiry policy: a different feature,
-- with a different failure mode (an administrator locked out of their own RADIUS
-- server at 2 a.m.), and it does not arrive by widening this column's meaning.
--
-- ── DEFAULT false, deliberately ──────────────────────────────────────────────
--
-- Every operator that exists when this migration runs was created with a generated
-- password, on the path this replaces. Defaulting to `true` would demand a password
-- change from people who never received a shipped credential -- a migration that
-- locks an existing deployment's administrators out of every screen until they
-- rotate a password that was already random. The flag says *"this account holds the
-- credential we shipped"*, and of those rows, none does.

-- +goose Up
ALTER TABLE operators
    ADD COLUMN must_change_password boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN operators.must_change_password IS
    'This operator holds the credential the install shipped with. While true the API '
    'refuses every operation but getAuthSession, signOut and changeOwnPassword. Set '
    'only by foyerctl create-operator --must-change-password; cleared only by a '
    'successful password change.';

-- +goose Down
ALTER TABLE operators DROP COLUMN must_change_password;
