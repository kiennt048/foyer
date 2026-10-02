-- The second, opted-in verifier that completes EAP-TTLS/PAP.
--
-- The problem it solves is measured, not argued: `rlm_pap` in FreeRADIUS 3.2.10 has
-- no argon2 scheme, and `Crypt-Password` hands the string to libxcrypt, which
-- returns its failure token `*0` for `$argon2id$`. So `docs/rules/security.md` §1's
-- recommendation of EAP-TTLS/PAP was right about the protocol and wrong about the
-- tool Foyer ships: an argon2id identity cannot authenticate over it.
--
-- ── the scheme is bcrypt, where yescrypt has the same property ──────────────
--
-- `docs/rules/security.md` §1 names either. **It has to be bcrypt, and the reason
-- is a hard constraint rather than a preference:**
--
--   * `golang.org/x/crypto` is already a DIRECT dependency and carries `bcrypt`.
--     There is no pure-Go yescrypt in it, and a new dependency is not added for
--     this.
--   * `deploy/Dockerfile.foyer` builds the control plane `CGO_ENABLED=0` on alpine.
--     yescrypt exists only in libxcrypt, which is reachable through `crypt(3)`,
--     which is reachable only through cgo. **Foyer could not produce a yescrypt
--     verifier at all** without changing both of those.
--
-- Measured (`docs/protocol-reality.md`), as real supplicant
-- conversations rather than as reasoning:
--
--     Crypt-Password = bcrypt  $2b$    EAP-TTLS/PAP SUCCESS    PEAP-MSCHAPv2 FAILURE
--     Crypt-Password = yescrypt $y$    EAP-TTLS/PAP SUCCESS    PEAP-MSCHAPv2 FAILURE
--
-- The two are equivalent for the property this verifier exists for. bcrypt is memory-
-- light where yescrypt is memory-hard, and that is a real difference this schema
-- does not hide.
--
-- Measured for this migration: libxcrypt accepts Go's `bcrypt` output
-- verbatim, under `$2a$` and under a `$2b$` relabel of the same digest, on Debian
-- sid AND on Ubuntu noble (libcrypt 1:4.4.36-4build1), for ASCII and for UTF-8
-- passwords alike. The CHECK below therefore admits both prefixes.
--
-- ── what this table is NOT ──────────────────────────────────────────────────
--
-- It is not a second `account_nt_hashes`, and the difference is the whole reason
-- this verifier was acceptable where PEAP-MSCHAPv2 is not:
--
--   * An NT hash is **password-equivalent** — it is the sole input to the MSCHAPv2
--     response, so whoever reads it authenticates. It is envelope-encrypted for
--     that reason, and `rlm_sql` therefore cannot read it, which is exactly why
--     PEAP-MSCHAPv2 stays blocked.
--   * A bcrypt verifier is **one-way**. Reading it does not authenticate. So it can
--     live in a column `rlm_sql` can read, which is what makes TTLS/PAP work.
--
-- It is therefore **not a second exception to "secrets are enveloped"**
-- (`docs/rules/database.md`). That rule is about material that is a credential
-- in cleartext — the `nas` projection's shared secret. This is a hash.
--
-- ── and it is not Password-With-Header, ever ────────────────────────────────
--
-- Measured, and the most dangerous thing in `docs/protocol-reality.md`: `rlm_pap`
-- rewrites an unrecognised `Password-With-Header` value to `Cleartext-Password`, so
-- an encoded hash BECOMES the known-good password. The generated query hands this
-- column to `Crypt-Password` and nothing else; `internal/raddb` has a test that
-- refuses to emit the other attribute at all.

-- +goose Up

-- The composite key the verifier table's foreign key needs. `id` is already the
-- primary key, so this adds no row-level guarantee to `accounts` — it exists so
-- that PostgreSQL, rather than a Go function, keeps the username copy below equal
-- to the username it copies.
ALTER TABLE accounts
    ADD CONSTRAINT accounts_id_username_key UNIQUE (id, username);

CREATE TABLE account_pap_verifiers (
    account_id text PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE
                    DEFAULT foyer_current_tenant(),

    -- A copy of `accounts.username`, and NOT a denormalisation somebody has to
    -- remember to maintain: the composite foreign key below makes PostgreSQL keep
    -- it equal, and cascade it on rename. `docs/rules/database.md` forbids
    -- restating a constraint in code that talks to the database; this is the shape
    -- that does not drift, because there is only one writer and it is the server.
    --
    -- It is here rather than joined because the UNIQUE below cannot be expressed
    -- across two tables, and that UNIQUE is load-bearing — see the view.
    username text NOT NULL,

    -- The bcrypt verifier, as `crypt(3)` will read it.
    --
    -- The CHECK is the same decision migration 00013 made for `password_hash`: the
    -- constraint belongs where the value lands, not in a validator. `$2a$`/`$2b$`,
    -- a two-digit cost, then exactly 53 characters of bcrypt's base64 alphabet —
    -- 22 of salt and 31 of digest.
    --
    -- What it makes impossible is the list worth reading: a cleartext password
    -- (which `rlm_mschap` would turn into an NT hash on the fly, making the
    -- identity MSCHAPv2-authenticable with no opt-in anywhere), an argon2id string
    -- (which libxcrypt answers `*0` for, so the identity would simply never
    -- authenticate and the failure would be blamed on the supplicant), and an NT
    -- hash (which belongs in the enveloped table or nowhere).
    -- Named, not inline-anonymous: a test asserting a refusal must be able to say
    -- WHICH constraint refused, or it is a test of whichever guard fires first —
    -- `docs/rules/database.md`.
    verifier text NOT NULL
        CONSTRAINT account_pap_verifiers_is_bcrypt
        CHECK (verifier ~ '^\$2[ab]\$[0-9]{2}\$[./A-Za-z0-9]{53}$'),

    -- The opt-in is a decision, so it is recorded like one. `account_nt_hashes`
    -- carries the same two columns for the same reason: docs/rules/security.md §1
    -- asks that no password is ever stored in a form that enables a method "without
    -- saying so explicitly", and a row whose existence nobody can date does not say
    -- it.
    opted_in_at timestamptz NOT NULL DEFAULT now(),
    opted_in_by text,

    CONSTRAINT account_pap_verifiers_account_fk
        FOREIGN KEY (account_id, username) REFERENCES accounts (id, username)
        ON UPDATE CASCADE ON DELETE CASCADE,

    -- ── globally unique, and this is the finding that shaped the table ───────
    --
    -- `accounts` is UNIQUE (tenant_id, site_id, username). FreeRADIUS's identity
    -- namespace is FLAT — the same fact that makes `nas.nasname` globally unique,
    -- one layer up. An 802.1X supplicant sends a username and nothing else; there
    -- is no site in the packet and no way to put one there.
    --
    -- So without this constraint, two sites in one install could each hold an
    -- `alice` with a verifier, the credential query would return two rows for one
    -- User-Name, and `rlm_pap` would verify the submitted password against a
    -- verifier from the OTHER SITE. That is a cross-site authentication bypass, it
    -- succeeds rather than fails, and nothing in the packet or the log would say
    -- which row answered.
    --
    -- The refusal is the honest cost and it can name what it collided with, which
    -- is the one thing `nas`'s flat namespace bought (`docs/rules/database.md`).
    CONSTRAINT account_pap_verifiers_username_is_global UNIQUE (username)
);

COMMENT ON CONSTRAINT account_pap_verifiers_username_is_global ON account_pap_verifiers IS
    'FreeRADIUS identity namespace is flat: an 802.1X supplicant sends a username '
    'with no site in it. Two opted-in accounts sharing a username would make the '
    'credential query ambiguous and let one site''s password authenticate another '
    'site''s identity. Same reasoning as nas.nasname being globally unique.';

ALTER TABLE account_pap_verifiers ENABLE ROW LEVEL SECURITY;

CREATE POLICY account_pap_verifiers_isolation ON account_pap_verifiers
    USING (tenant_id = foyer_current_tenant())
    WITH CHECK (tenant_id = foyer_current_tenant());

GRANT SELECT, INSERT, UPDATE, DELETE ON account_pap_verifiers TO foyer_app;

-- ── the minimally-privileged view rlm_sql reads ─────────────────────────────
--
-- `docs/rules/security.md` §2: FreeRADIUS fetches password verifiers itself via
-- `rlm_sql` from a minimally-privileged view, and nothing crosses the `rlm_rest`
-- boundary. This is that view, and `foyer_radius` gets SELECT on it and on nothing
-- else here — deliberately NOT on `account_pap_verifiers`, so the data plane cannot
-- read `opted_in_by`, cannot see a disabled account's verifier, and cannot join to
-- anything.
--
-- ── why it is not security_invoker, stated because the opposite is the trap ──
--
-- `docs/rules/database.md` has a section on `nas` having no RLS policy: an unset
-- tenant GUC matches no row, FreeRADIUS has no concept of a tenant and never sets
-- it, and a policy there yields a server that loads no clients and discards every
-- packet — total failure that reads as a network problem.
--
-- The same trap is here, one table over, and it would read as *every corporate
-- laptop's password is suddenly wrong*. The escape is different because the table
-- must keep its policy for `foyer_app`: a view is not `security_invoker`, so its
-- query runs as the view's owner — the migration runner, which owns the table and
-- therefore bypasses RLS (no table here uses FORCE ROW LEVEL SECURITY). The
-- property is asserted from the `foyer_radius` role in a test rather than assumed
-- from that paragraph.
--
-- `enabled` is applied HERE rather than in the generated query, because a query
-- lives in emitted configuration and this lives in the schema. A disabled account
-- with a verifier must not authenticate, and the place that cannot be forgotten in
-- a regenerated tree is this one.
CREATE VIEW radius_pap_verifiers WITH (security_invoker = false) AS
    SELECT v.username, v.verifier
    FROM account_pap_verifiers v
    JOIN accounts a ON a.id = v.account_id
    WHERE a.enabled;

COMMENT ON VIEW radius_pap_verifiers IS
    'The only credential surface rlm_sql may read. security.md 2: authorization '
    'crosses rlm_rest, credentials never do. Two columns, no account id, no '
    'opt-in provenance, and disabled accounts are absent rather than filtered '
    'downstream.';

GRANT SELECT ON radius_pap_verifiers TO foyer_radius;

-- +goose Down

DROP VIEW radius_pap_verifiers;
DROP TABLE account_pap_verifiers;
ALTER TABLE accounts DROP CONSTRAINT accounts_id_username_key;
