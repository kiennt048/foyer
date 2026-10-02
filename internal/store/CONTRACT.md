# internal/store — CONTRACT

The schema, and the invariants the rest of the product is allowed to assume.

Two of them are enforced by PostgreSQL rather than by this package's code, and that
distinction is the point of the section headings below: a rule the code keeps is a
rule the next caller can forget.

---

## Enforced by the database

### 1. `radacct` and `radpostauth` are read-only to the application

FreeRADIUS owns both tables. The application **logs in as** `foyer_app`, which holds
**`SELECT` and nothing else** on them. An `INSERT`, `UPDATE` or `DELETE` from Go is
refused by PostgreSQL with SQLSTATE `42501`, whatever the Go code says.

`readonly_test.go` drives every refusal and asserts the error names **the table**,
not merely that something failed: `radacct.radacctid` is a `bigserial`, so an
`INSERT` can be refused by the *sequence* even when the table is open, and a
SQLSTATE-only assertion cannot tell "the table is protected" from "the table is wide
open and the sequence refused".

There are **two** audited exceptions. **The first is the stale-session reaper**,
which runs as
`foyer_reaper` with column-level `UPDATE` on `acctstoptime`,
`acctterminatecause`, `acctsessiontime` and `acctupdatetime`. It cannot insert,
cannot delete, cannot touch a byte counter, and has no privilege at all on
`radpostauth`.

**The second is retention and erasure**, a deliberate, narrow extension of the
first. It runs as `foyer_retention` (migration 00020): `SELECT` and
`DELETE` on `radacct`, `radpostauth`, `audit_log` and
`authorization_events`, and nothing else — no `INSERT`, no `UPDATE`, no `TRUNCATE`, no
sequence — plus column-level `SELECT` on `radius_clients (tenant_id, site_id, address)`
and `access_points (tenant_id, site_id, mac)`, which is how an erasure by account tells
its venue's rows from a same-named guest's at another venue (usernames are unique per
venue, 00005). Every statement it runs is a constant in **`retention.go`**, the one Go file
the write-time guards let name a `DELETE` on the FreeRADIUS tables. `OpenRetention`
reads the connection's privileges after `SET ROLE` and refuses one that can also insert,
rewrite or truncate either table — a superuser included — so the privilege the file runs
with is the one it claims. `privacy_test.go` drives every refusal and names the table.

### 2. The stock column set is verbatim

`radacct` and `radpostauth` carry FreeRADIUS 3.2.x's columns exactly. Indexes,
partitioning and retention are ours; **the column set is not.** It is the project's
exit hatch: if Foyer disappears, the accounting history stays readable by a stock
FreeRADIUS installation.

The consequence is that **neither table carries `tenant_id`**, so neither can carry
a row-level policy. Tenant scoping on the read path is derived instead: every query
in `accounting.go` joins `radius_clients` by longest-prefix match on the NAS
address, and `radius_clients` does carry a tenant and does have a policy. That join
is inner, so a row whose NAS matches no client disappears — which is why
`UnmatchedClientAddresses` exists. A silent drop would be a console that shows
nothing and means nothing.

`radpostauth.pass` exists because the stock schema has it. **Nothing writes it.**
The generated post-auth query omits it, and `AuthAttempt` has no field a submitted
credential could arrive in.

### 3. The audit log is append-only

`GRANT SELECT, INSERT ON audit_log` — no `UPDATE`, no `DELETE`, and the row-level
policies exist only for those two commands **for the application**. The one `DELETE`
policy (00020) is scoped `TO foyer_retention` and is what the age-based sweep uses;
nothing removes one entry. `actor_username` is denormalised so an
entry still says who did the thing after that operator is deleted.

**An erasure pseudonymises earlier entries; it never deletes one** (00052). The one
UPDATE path is `foyer_audit_pseudonymise`, a SECURITY DEFINER function owned by the
NOLOGIN role `foyer_audit_redactor` (SELECT, and UPDATE of `subject_id`,
`subject_name`, `params` only, with an UPDATE policy scoped to it). `foyer_app` holds
EXECUTE on it and still **no UPDATE** on the table. The function only ever replaces a
MAC-shaped match (any spelling, not inside a longer hex run) or an exactly-named
identifier of three characters or more with a string of the form `pseudonym:` + 16
hex; a literal is replaced only on guest entries (`session`, `portal-grant`,
`account`, `marketing-consent`, `data-subject`). The pseudonym is HMAC-SHA256 under an
HKDF subkey of the active KEK (`auditpseudonym.go`, label `foyer/audit-pseudonym/v1`);
with no keyring it is random per erasure. The eraser is registered at Order 100 with
`Pseudonymises`, so its count is reported in `ErasureReport.Pseudonymised`, never in
`Removed` or `Total`.

**The erasure's own entry is written in the erasure's transaction** (`EraseRecorded`):
after every application-side eraser and before the commit, so an entry that cannot be
written rolls the Foyer side back. The FreeRADIUS side has committed before it (two
roles, two connections) and is idempotent; a retry records it.

### 4. Every tenant-scoped table has a row-level policy

Keyed on `foyer_current_tenant()`, which reads the `foyer.tenant_id` GUC that
`store.Open` sets on every connection in the pool. **An unset GUC is NULL and
matches no row**, so a forgotten scope yields an empty result rather than another
tenant's data.

**Every policy is `FORCE`d** (migration `00019`), so a table's owner is subject to it
too unless the owner is a superuser or `BYPASSRLS` — which the migration runner in
`deploy/` is, and which is what migrations, backup and restore run as. A table added
with `ENABLE ROW LEVEL SECURITY` and no `FORCE` fails `TestEveryRowLevelPolicyIsForced`.
The application is not the owner and owns nothing; `NewStore` refuses a login that does.

### 4a. Nobody but the migrator logs in as more than its grants

The login **is** the privilege role (migrations `00019` and `00021`), so `RESET ROLE`
returns to the same privileges:

| Login | Used by | Holds |
|---|---|---|
| the schema owner | `foyerctl migrate`, backups | everything |
| `foyer_app` | `cmd/foyer`, `foyerctl` in the control-plane container | its grants |
| `foyer_radius` | FreeRADIUS `rlm_sql` | SELECT `nas`, SELECT `radius_pap_verifiers`, write `radacct`/`radpostauth` |
| `foyer_reaper`, `foyer_retention` | the maintenance service | the two audited exceptions in §1 |

- **`NewStore` refuses any login that is more than `foyer_app`** —
  `ErrOverPrivilegedLogin` for a superuser, `BYPASSRLS`, `CREATEROLE`, `CREATEDB`,
  `REPLICATION`, membership in any other role, or owning a relation. `Open` still
  issues `SET ROLE` (harnesses seed as the owner) and nothing relies on it.
- **Passwords are never in the schema.** `00019` grants LOGIN; `SetLoginPassword`,
  called by `foyerctl migrate` from `FOYER_APP_DB_PASSWORD` /
  `FOYER_RADIUS_DB_PASSWORD`, sets a client-computed SCRAM-SHA-256 verifier, so the
  cleartext never reaches the server. It refuses NOLOGIN roles rather than granting
  LOGIN itself, so provisioning cannot paper over a role that lacks it.
- **`radius_pap_verifiers` is owned by `foyer_credential_view`** (NOLOGIN, not a
  superuser): column-level SELECT on the five columns its query names, and a
  SELECT-only `USING (true)` policy for that role alone on `accounts` and
  `account_pap_verifiers`. A superuser owner would run the "minimally-privileged
  view" with every privilege, and under `FORCE` an ordinary owner would see no row
  without the tenant GUC.
- `loginroles_test.go` drives every one of these through a **real login**, not
  `SET ROLE` from the owner — `storeAsRole` is that arrangement and cannot see a
  defect in it.

v1 ships single-tenant: one row in `tenants`, and nothing in the product offers to
create a second.

---

## Enforced by this package

### 5. Credential material is envelope-encrypted, and bound to its row

RADIUS shared secrets, controller credentials and NT hashes are sealed with a
per-row AES-256-GCM data key, itself wrapped under a key-encryption key read from
`FOYER_KEK` — **outside the database**.

Every seal is bound to `table/column/row-id` as associated data, so a ciphertext
lifted from one row and pasted into another fails to open. Without that, anyone with
`UPDATE` on `controllers` could move a known-good credential onto a controller they
registered themselves.

No keyring configured is **not** silently no encryption: those methods return
`ErrNoKeyring`.

**One cleartext copy exists and is declared.** `nas.secret` holds the shared secret
in the clear, because FreeRADIUS validates a Message-Authenticator against the
secret itself and has no way to ask Foyer for it. `shared_secrets` is the system of
record; `nas` is a projection generated from it, readable only by `foyer_radius`.

### 5a. The `nas` projection has exactly one writer, and it removes as well as adds

`NasProjectionRepo.Sync` is it. The write surface is small enough to state, and it
is stated because a synchronisation between two tables is the classic place for a
function that runs, returns `nil` and copies nothing:

- Reads `radius_clients` (enabled only) under RLS and unseals each secret through
  `SharedSecrets().Reveal` — one call per client rather than a join, because the
  ciphertext is bound to `table/column/row-id`. **No keyring is `ErrNoKeyring`,
  never a projection with the secrets left out** — a client table written without
  secrets authenticates nothing and looks fully populated. The read happens before
  the transaction, so a broken keyring fails before anything is written.
- INSERTs absent rows, UPDATEs rows whose five projected columns differ **or whose
  `tenant_id` is NULL**, and rewrites nothing else.
- DELETEs every row not in the wanted set whose `tenant_id` is this tenant's **or
  NULL**, and **reports what it deleted by name** — `NasSync.Removed` is `[]string`
  of `nasname`, not a count. A decommissioned access point that keeps its RADIUS
  client is a security defect rather than untidiness, and a *count* of them cannot be
  handed to `raddb.TableBefore`, which is the only way `Classify` can see a row that
  was written after the last restart and deleted before the next one. Without the
  names that revocation classifies as `none` — *the deletion took effect* — and on
  3.2.10 it has not. `TableBefore` is variadic for the same reason this is a slice:
  *nobody looked* and *nothing was removed* are opposite answers.
- One transaction. A refused sync writes nothing at all, including the clients that
  had no conflict.

**`NasProjectionRepo.Plan` is the same classification, read-only.** It exists so a
diagnostic can answer *"is the projection current?"* without writing and without
re-implementing the naming rule outside this package — a comparison of both sides as
`netip.Prefix` cannot disagree about anything except the address, so a row whose
shortname or **shared secret** had drifted would read as current. `Sync` and `Plan`
share `classifyProjection`, because two
answers to "what would this write" that can disagree are worse than one that is
wrong. Without a keyring `Plan` still answers, with `SecretsCompared: false` — the
one thing it may never do is report agreement about a column it did not read.

`tenant_id IS NULL` means *no sync produced this row* — the shape
`lab/blue-green/seed-client.sh` and `lab/policy/sigstop_test.go` write, over a
connection that never sets the `foyer.tenant_id` GUC. It is a real state, not a
missing value: `NOT NULL` here refuses those writers outright, which is measured in
migration `00008`'s own comment.

**A NAS address held by another tenant is refused, never taken over.**
`nas.nasname` is globally unique because FreeRADIUS's client list is one flat
namespace and `nasname` is the value the server matches a packet's source address
against; `radius_clients` is `UNIQUE (tenant_id, address)`. Only one of two tenants
sharing `192.168.1.1` can ever be projected. Last-writer-wins would point one
tenant's access points at another tenant's shared secret — a cross-tenant
authentication bypass with an audit trail that looks entirely normal — so `Sync`
returns `*NasAddressConflict`, which carries a field per fact so an operator can be
shown which address, which two tenants and which client on each side.

**`Rows` is the tenant's view; `AllRows` is the startup snapshot.** FreeRADIUS reads
this table with `read_clients = yes` over a connection with no tenant GUC, so it
loads every row there is. A snapshot built from `Rows` omits rows the server did
load, and an omitted row classifies as *new* — `sql-only` inside a dynamic network.
The operator is then told a secret rotation needs no restart while the running
server refuses every packet on the new secret.

### 6. NT hashes live in their own table, for identities that opted in

An NT hash is unsalted MD4 and is the sole input to the MSCHAPv2 response: a stolen
one authenticates directly. It is **password-equivalent**, not a hash to shrug at.

`account_nt_hashes` is separate from `accounts`, so no ordinary account query can
leak one. `Account.CredentialKind` (`argon2id` / `argon2id+nt`) is computed from
whether a row exists there — a flag on the account could be wrong; a `LEFT JOIN`
cannot. The method that writes one is called `OptInToMSCHAPv2`, because storing it
is a decision rather than a detail, and `opted_in_by` records who made it.

**CHAP is absent from this schema and must stay absent.** It would require cleartext
storage for a legacy checkbox.

### 6a. The 802.1X verifier lives in its own table too, and it is bcrypt

`account_pap_verifiers` is what lets FreeRADIUS verify a password at all — a NAS's
PAP login today, and **EAP-TTLS/PAP** when the generator emits it: measured on
FreeRADIUS 3.2.10, `rlm_pap`
has no argon2 scheme and libxcrypt answers its failure token for `$argon2id$`, so
an identity whose only verifier is argon2id cannot authenticate over it.

**It is not a second `account_nt_hashes`, and the difference is the whole reason
this is allowed where PEAP-MSCHAPv2 is not.** An NT hash is password-equivalent, so
it is enveloped and `rlm_sql` therefore cannot read it. A bcrypt verifier is
one-way: reading it does not authenticate, so it may live in a column the data
plane can read. **It is therefore not a second exception to §5** — that rule is
about material that is a credential in cleartext.

Four things the database enforces rather than this package:

- `CHECK account_pap_verifiers_is_bcrypt` — `$2a$`/`$2b$`, a two-digit cost, 53
  characters. Cleartext, an argon2id string, an NT hash and a
  `{crypt}`-prefixed form are all refused, and each would fail differently and
  badly.
- **`UNIQUE (username)`, globally.** FreeRADIUS's identity namespace is flat: an
  802.1X supplicant sends a username with no site in it. Two opted-in identities
  sharing one make the credential query ambiguous, and that failure *succeeds* —
  one site's password authenticating another site's identity. `ErrPAPUsernameTaken`
  names the site it collided with.
- A **composite foreign key** to `accounts (id, username)` with `ON UPDATE
  CASCADE`, so the username copy the UNIQUE needs is kept equal by PostgreSQL
  rather than by a Go function that would drift.
- The view **`radius_pap_verifiers`** — two columns, `GRANT SELECT` to
  `foyer_radius` and to nothing else, disabled accounts absent. It is deliberately
  **not** `security_invoker`: every table here carries an RLS policy, FreeRADIUS
  never sets the tenant GUC, and an invoker-rights view would return zero rows —
  the `nas` trap one table over, and it would read as every corporate laptop's
  password being wrong. Asserted from the `foyer_radius` role in a test. The
  view's owner is `foyer_credential_view`, not the superuser — §4a.

`Account.CredentialKind` gains `+bcrypt`, computed from the `LEFT JOIN` for the same
reason `+nt` is. The method is `OptInTo8021X`.

### 7. Argon2id everywhere else, and PostgreSQL enforces it

`operators.password_hash` and `accounts.password_hash` are argon2id. This package
never sees a cleartext password: callers hash in `internal/identity` and pass the
verifier.

That is a **CHECK constraint** (migration `00013`) rather than a habit —
`password_hash LIKE '$argon2id$%'` on both tables. A validator in Go would be a
second source of truth for a rule the database can own, so the test attempts the
write and lets PostgreSQL refuse it, matching on the SQLSTATE *and* the constraint
name.

**What the constraint is protecting against is not what the sentence sounds like.**
`docs/rules/testing.md` names *"no argon2-hashed identity is ever reachable by
MSCHAPv2"*, and measured against FreeRADIUS 3.2.10 with a real supplicant
(`docs/protocol-reality.md`) that half holds by physics: an identity whose
only verifier is a one-way hash is refused with *"FAILED: No NT-Password"*, for
argon2, bcrypt and yescrypt alike. The way it stops holding is a value in this
column that is **not** a one-way hash:

- **cleartext** — `rlm_mschap` derives the NT hash from `Cleartext-Password` on the
  fly, so one such row makes that identity MSCHAPv2-authenticable with no opt-in
  and no `account_nt_hashes` row for `CredentialKind` to report;
- **a scheme FreeRADIUS does not recognise** — worse, because `rlm_pap` rewrites an
  unknown `Password-With-Header` to `Cleartext-Password`, so **the stored hash
  becomes the password.** Proven with an `eapol_test` run that authenticated by
  sending the stored argon2id string.

The naming assertion in the test is not decoration either: an operator row with an
invalid `role` is refused by `operators_role_check`, so a subtest that matched only
the SQLSTATE would pass on the wrong constraint — and go on passing with the
credential CHECK removed.

### 8. The three nouns stay three tables

`controllers`, `radius_clients` and `access_points` are separate, per
`docs/architecture.md`. Their counts, identifiers and failure
states all differ:

| | identified by | created by | cardinality |
|---|---|---|---|
| controller | URL | the operator, once | 1 |
| shared secret | — | derived, per RADIUS profile | 1 |
| RADIUS client | IP or CIDR | derived from the device list | N |
| access point | MAC | discovered | N |

`Nas` is a **read model**, not a table: `radius_clients LEFT JOIN access_points`,
one row per device. `Nas.ID` is the access point's where there is one and the
client's where there is not; `Nas.RadiusClient.ID` always reaches the clients.conf
row, because rotating a secret and generating config act on the client rather than
the device.

`shared_secrets` is many-clients-to-one-secret because the secret is a property of a
controller-level RADIUS profile. A secret column on the client row would be N
secrets against a controller with one field to type one into.

**`radius_clients.coa_vendor` is nullable and NULL is an answer** (migration 00015).
`driver = 'coa'` is one value for Cisco and Aruba, which identify a session
differently, so the column says which; every other driver names its vendor by naming
itself and carries NULL. NULL on a `coa` row means *nobody has said*, and such a row is
**refused a driver by name** rather than given a guessed one — the wrong dialect
identifies no session, and a careless NAS answers that with an acknowledgement and no
effect, which is the one CoA failure the lab cannot produce.

It is deliberately not `NOT NULL`-for-`coa`: a constraint that strong would make a
migration invent a vendor for every row already stored as `coa`, which is the
guess the column exists to prevent, taken once, in SQL, on somebody else's estate. It
is also **not** a third inert switch beside `nas.require_message_authenticator` and
`nas.limit_proxy_state` — its writer (`NasCreate.coaVendor`) and its reader
(`apistore.VendorDrivers.ForNAS`) both exist.

An access point that discovery stops seeing is marked `missing`, never deleted:
deleting it would drop the MAC that resolves last week's session rows.

### 8a. The portal's two tables, and the lookup that is deliberately absent

`portal_sessions` and `portal_grants` (migration `00010`) are specified in
[`internal/portal/CONTRACT.md`](../portal/CONTRACT.md), which is their authority the
way `docs/architecture.md` is 00004's. Three properties are load-bearing:

- **`portal_sessions` is keyed on the token hash and there is no lookup by MAC.**
  Not "there is no method for it" — there is no index for it either, because an
  index is an invitation. A MAC arrives from the NAS as an unsigned query parameter
  and resolving a browser's state from a value somebody typed is the whole of
  `docs/rules/security.md` §4. The expiry lives in the read's `WHERE` clause, so
  an expired row and a token that never existed are one `ErrNotFound` by
  construction rather than by a comparison somebody remembered to write.
- **`portal_grants_one_per_voucher` is the single-use guarantee as a constraint.**
  §9 asks for single use enforced transactionally; `ClaimVoucher` is one
  `UPDATE … WHERE state = 'unused'` joined to the batch, and the partial unique
  index is the version that survives a future caller who writes the read-then-write
  anyway. Making the index **total** rather than partial changes no behaviour here, because PostgreSQL treats NULLs as distinct in a
  unique index — the `WHERE` clause buys index size, and `NULLS NOT DISTINCT` is
  what would break click-through.
- **The plan is copied onto the grant with no foreign key**, and `enforced_by` is
  stored rather than derived. A batch edited afterwards must not change what a guest
  was already sold, and `enforced_by` is a fact about the moment the guest was let
  on — the driver's `HardQuota` as it was then.

Two things the CONTRACT's DDL asks for that this schema does not carry, both
deliberate: every id is `text` rather than `uuid`, because nothing it references has
a uuid primary key and `portal_grants.id` comes from `internal/portal`'s own
generator; and `vouchers.claimed_mac` does not exist, because `portal_grants`
already records which device was let on and a second copy is a second source of
truth. `vouchers.current_session_id` is **not** that column — it names a `radacct`
row, which does not exist at the moment a voucher is claimed.

### 8b. `authorization_events` and `portal_grants.delivery` are one write

Migration `00014`, specified in [`internal/portal/CONTRACT.md`](../portal/CONTRACT.md)
invariant 10. They are the operator's and the guest's readers of **one fact** — that
the controller was asked to open the network and what it answered — and
`GrantRepo.RecordAuthorization` writes them in one transaction because an
implementation able to write one without the other could show the console
`authorize-failed` beside a portal showing a green tick.

Four properties are load-bearing and three of them are the database's:

- **The `UPDATE` goes first.** A grant that does not exist answers `ErrNotFound`
  naming the grant, and no event row survives the rollback. The foreign key would
  also refuse the INSERT, with a constraint name a caller cannot act on.
- **`delivery` is nullable and NULL is the zero value on purpose.** It covers *the
  driver call has not returned yet* — which is what a successful redemption looks
  like for the length of one controller round trip, because the redirect is flushed
  first — and *this deployment wired no `Authorizations` port*. Both render
  `connected`, because *nothing has told us it failed* is not *it failed*.
- **An outcome cannot disagree with its explanation.** Two `CHECK` constraints:
  `failed` iff there is a failure kind, and a `status` only where something
  answered. Both are in the schema rather than in Go, because a validator that
  re-implements a rule the database could own is a second source of truth.
- **`foyer_app` holds `SELECT, INSERT` and nothing else**, plus `USAGE` on the
  sequence — which is not boilerplate: an `INSERT` on a bigserial table calls
  `nextval()` (§1). Without the sequence grant the writer records nothing and
  PostgreSQL names the sequence rather than the table.

The read is **not** in `GrantRepo`. `AccountingRepo.ListAuthAttempts` unions this
table with `radpostauth` because `listAuthAttempts` is one operation and a page has
one cursor: a caller that read a page from each and merged them in Go could not name
a resume point inside either. So each half lives with the table it cannot be
separated from — the write with `portal_grants`, the read with `radpostauth`.

It is a **Foyer-owned table and not an extension of `radpostauth`**, whose column set
stays stock because it is the exit hatch (§2).

### 8c. The plan behind a session is a key join, and the better source is refused

`SessionPlanRepo` (`sessionplan.go`) answers *what was the identity in this
accounting row sold?* It owns no table: it reads `vouchers → voucher_batches` and
`accounts`, both keyed on `(site_id, username)` against `radacct.username`, which for
a redeemed voucher is the code the NAS put in `User-Name` and for a guest account is
the account's name. Absent from its answer means **there is no plan** — the honest
result for MAC-auth, for 802.1X, and for a name Foyer does not own.

Two properties, and the second is the invariant:

- **The site is half the key.** `accounts` is `UNIQUE (tenant_id, site_id,
  username)`, so a lookup on the username alone resolves one venue's guest against
  another venue's allowance the first time two sites both employ a *reception*.
- **`portal_grants` is the better source and is deliberately not used.** Its plan is
  the snapshot this guest was actually sold, `enforced_by` included, and it has no
  key to a `radacct` row and never will — §2 keeps the stock column set verbatim, and
  a grant exists before any accounting row does. What is left to join on is
  `(site_id, client_mac, a time window)`, which is an inference over a per-SSID
  pseudonym presented as a fact. A wrong plan on a session row is worse than no plan
  because nothing on the screen can contradict it.

### 8d. `portal_settings.design` is opaque here, and a missing value keeps what is stored

Migration `00018`. One `jsonb` column holding the contract's `PortalDesign` verbatim,
`NOT NULL`, defaulting to the empty design. This package does not import the generated
types and does not validate the shape — `internal/api` does, before the write, and a
`CHECK` restating it would be the second copy `docs/rules/database.md` warns about.
`PortalRepo.Upsert` treats a **nil** `Design` as *not supplied*: a new row takes the
empty design, an existing row keeps its own. Site creation upserts a row knowing only
a venue name, and that path must not be the one that wipes a venue's page. Every other
column in the upsert is still overwritten; the asymmetry is deliberate and pinned by
`TestPortalSettingsAndHeroImage`.

### 9. Migrations are the only way the schema changes

`goose` v3, SQL embedded from `migrations/`, applied by `foyerctl migrate`. Adding
a column means adding a migration; there is no other path and no schema file.
Every migration has a `Down`, and `migrate_test.go` walks the set down to
zero and back up.

Migrations run as the schema owner. `foyer_app` cannot create a table, so pointing
the migrator at the application's credentials fails outright rather than
half-applying.

---

### `operators.must_change_password` is cleared in the same statement that sets the password

`SetPassword` writes `password_hash` **and** clears the flag in one `UPDATE`. Two
statements can produce the state where the change succeeded and the flag did not
clear, and that state is a console refusing every screen after the operator did
exactly what it asked — with no way out, because `changeOwnPassword` would then want
a `currentPassword` that is no longer current. It is the same argument
`docs/rules/security.md` §9 makes about single-use vouchers, one table over.

Nothing in this package sets the flag. `foyerctl create-operator
--must-change-password` is the only writer, and migration 00017 says why a *server*
deciding a password must change is a different feature rather than a wider reading of
this column.

### 10. Every personal-data column is registered, with how it is erased

`privacy.go` holds three registries: `PersonalColumns` (every column holding or
describing a guest identifier), erasers (per table, per subject kind), and retention
categories (per kind of data, with defaults). `privacy_test.go` reads the **live schema**
and fails for any column whose name or type looks like a MAC, a username, a phone number
or an e-mail address that is not registered — with the subject kind that erases it, the
cascade that does, or `NotGuestData` and the reason. A feature that adds such a column
registers it, an eraser, and usually a retention category, from an `init` in its own
file (`RegisterPersonalColumn`, `RegisterEraser`, `RegisterRetentionCategory`);
`RegisterEraser` can only place an eraser on the application's connection.
`docs/operator/personal-data.md` is held to the same registry by the same test.

**Four subject kinds** (`SubjectMAC`, `SubjectAccount`, `SubjectPhone` and
`SubjectEmail` — `guest_details.email` and `marketing_consents.email`, matched
case-insensitively, plus the audit log). A phone subject is the number's digit strings
— international and, for a Vietnamese number, national — compared with every phone
column reduced to its digits, and its exact spellings (`+84…`, `84…`, `0…`) compared
with usernames; it erases at every venue, because a number is one person's wherever
they used it. The phone columns (`phone_codes.phone`, `portal_grants.phone`,
`guest_details.phone`, `marketing_consents.phone`) and the usernames identify it; the
portal sessions, address book entries and consents bound to its grants go with them.

**The completeness test is the schema, not the registry** (`erasurecoverage_test.go`):
for each kind it seeds the subject everywhere, erases, and reads every text, varchar,
inet, cidr, macaddr, json, jsonb and text-array column of every table and view,
failing on any value that still spells the identifier. The name-based registry test
remains as the guard that makes a new column *declare* how it is erased.

**Every retention category's minimum is held by the database** (00051):
`retention_policies_floor CHECK (days >= foyer_retention_floor(category))`, the same
figures as `MinDays`, and `SweepRetention` re-applies `MinDays` itself (logged at ERROR)
if a stored period is below it. `TestTheDatabaseHoldsEveryRetentionFloor` fails for a
category whose two floors disagree.

An erasure matches MACs by **normalised value**, never by the spelling list
`ActiveSessionForMAC` uses: a miss there fails closed, a miss here leaves a row behind
an erasure the operator was told succeeded.

**A session's `IdentityKind` is recorded, not guessed** (`recordedIdentityKind`): a
voucher at the session's venue whose code is the username exactly, else an account
with that exact username, else a voucher whose code is the username normalised (upper
case, separators dropped); the username's shape is the fallback only for a name no
table holds. The session export masks a voucher code on this, so a code a NAS
respelled is still masked.

**A grant is aged from when it ended, never merely from when it was granted**
(`grantEnded`): expiry or revocation; else the same device's next grant at
the venue; else the device's newest accounting activity on one of the venue's NAS
(`macSpellingsSQL`, the eight Calling-Station-Id spellings, read-only); else
`granted_at`. An unlimited grant whose device is still heard from is kept.

### The report reads are visitors over a cursor (`reports.go`)

`SessionsStartedBetween`, `VouchersActiveBetween` and `GrantsBetween` hand one row at
a time to a visitor and hold nothing; the visitor's error stops the scan and closes
the cursor. They are methods on the repositories that own the tables, so the
session export reuses `sessionColumns`, `scanSession` and the longest-prefix client
join rather than restating them — an export whose `state` disagreed with
`listSessions` would be a second copy of the same rule.

`AccountingRepo.Trends` answers one row per bucket, **including the empty ones**
(`LEFT JOIN` from the bucket list), and counts a device once per bucket on its
twelve hex digits across `radacct` and `portal_grants`. Bytes are the reconstructed
totals of sessions that *started* in the bucket and reported a counter; the oldest
sample is taken over the **open** ones only. Migration `00024` adds the range
indexes the two tables Foyer owns lacked; `radacct` gets none, its `acctstarttime`
index is 00002's.

Read-only like everything else here on `radacct`: the role could not write it.

### Guest details and marketing consent are two tables, and a consent is only ever a ticked box (`privacy_guestdata.go`)

`guest_details` is what a guest typed, keyed by its grant and deleted with it
(`ON DELETE CASCADE`) — so the `portalGrants` retention period, an erasure of the
device and an erasure of the grant's account all reach it without an eraser of its
own. `marketing_consents` holds a row only for a guest who ticked the box, with the
contact copied onto it; `RecordConsent` inserts **from** `marketing_consent_texts`,
so a version the venue never published inserts nothing
(`ErrUnknownConsentVersion`) and the language is the text's own. A published text
is never updated or deleted by the application (SELECT, INSERT only), because a
consent names it. **Withdrawal clears the contact in the same UPDATE that marks the
row withdrawn**, and a CHECK in 00023 forbids a withdrawn row holding one. The
eraser runs at Order 15 — before the grants. **An account's consents are found by
`marketing_consents.account_id`** (00051), written by `RecordConsent` from the
grant it was given with, and through a surviving grant for older rows: `grant_id` is
SET NULL once the grant ages out, so without `account_id` an account erasure would
leave a consent behind. `account_id` is itself SET NULL when an account goes by any
other path — and the `marketingConsents` category sweeps by
`coalesce(withdrawn_at, consented_at)`. All four tables of 00023 FORCE row-level
security. The file sorts after `privacy.go` on purpose: `init` order is file order,
and the category is registered after the five in `privacy.go`.

## What this package will not do

- Write `radacct` or `radpostauth` outside the two audited exceptions of §1: the
  stale-session reaper (`internal/session`, as `foyer_reaper`) and `retention.go`
  (as `foyer_retention`).
- Return credential material from a method whose name does not say so.
  `Reveal`, `Credential` and `NTHash` are the three, and each documents its callers.
- Generate a voucher code, a shared secret or a token. Those are `crypto/rand` in
  `internal/identity`; this package stores what it is given.
- Ship a default anything. No default password, no default RADIUS secret, no
  default KEK.

## Phone sign-in (migration 00022)

- **`phone_sign_in`** — one row per site. `api_key` and `secret_key` are sealed under
  the keyring (`SecretContext("phone_sign_in", column, site_id)`), and the row cannot
  exist without both (`Upsert` refuses the first write with `ErrSecretUnset`).
  `daily_limit` is NOT NULL with no default: there is no value of *unlimited*.
- **`phone_codes`** — the code's HMAC (`code_mac`, 32 bytes) and the keyring entry that
  made it; never the code. **No foreign key to `portal_sessions`**, deliberately: a
  cascade would delete a browser's codes when its session is forgotten on a MAC change,
  and with them the rows the per-number limit counts. `CHECK (attempts BETWEEN 0 AND
  max_attempts)` backs the WHERE clause that spends attempts.
- **`phone_code_sends`** — the ceiling's counter, a conditional upsert per site and day
  that refuses at the limit. A counter rather than a `COUNT` over `phone_codes`, so the
  ceiling does not reset when the personal-data rows are reaped.
- **`PhoneRepo.Issue` is one transaction under `pg_advisory_xact_lock` per site and
  number**; `Attempt` returns the MAC only from the UPDATE that counted the attempt.
- `portal_grants.phone` (E.164) is held to `method = 'phone'` by a CHECK, and `phone` is
  a member of both method checks.
- `foyer_radius` has no grant on any of the three.

## Network settings (migration 00030)

`NetworkSettingsRepo`: `network_settings` (one row per tenant), `network_certificates`
(portal-tls, eap-tls), `radius_client_coa_timing` (per `radius_clients` row, cascade)
and `radius_operations` (the last sync and apply). All four are RLS-forced and
`foyer_app`-only; `foyer_radius` has no grant on any.

- **A private key is sealed under the KEK** (`private_key_sealed`, bound to its
  purpose by the associated data) and is refused with `ErrNoKeyring` rather than
  stored in the clear. `Certificate` never returns it; `CertificateKey` is the one
  reader.
- **A certificate and the address it is served at are written in one transaction**
  (`PutPortalCertificate`), and an EAP-TLS certificate with the stamp that makes the
  running server's copy stale (`PutEAPCertificate`).
- **The bounds are the API's.** The CHECKs refuse only nonsense at any bound, so the
  two cannot disagree about a legitimate value.

## Messaging settings (migration 00033)

- **`sites.time_zone`** — the site's IANA zone, NOT NULL, default `Asia/Ho_Chi_Minh`,
  so every daily figure counts the venue's day rather than the container's UTC day. The
  CHECK bounds the shape and refuses `Local`; whether the zone database knows the name is
  the API's check (`api.LoadTimeZone`). **`sites.time_zone_changed_at`** (00044) is when an
  operator last changed it — NULL for the migration's default, and not moved by a write
  of the zone the site already has or by any other change to the row. It is what
  `getSiteTimeZone.updatedAt` answers; `sites.updated_at` is the row's, not the zone's.
- **`phone_abuse_controls`** — one venue's tuned limits and mobile ranges; no row is the
  defaults. Every bound is a CHECK, none admits NULL, `per_number_day >= per_number_hour`,
  and `mobile_prefixes` is a non-empty subset of the Vietnamese mobile plan.
- **`phone_spend_cap`** (one row per tenant, NULL is no cap) and **`phone_code_sends_total`**
  (per tenant and day) — §9's deployment-wide cap. `countSend` increments the per-site and
  the total counter in the caller's transaction, both conditional, and counts the total
  whether or not a cap is set. `CountSend` is the same charge for a message that is not a
  code. Neither table names anybody; neither is reaped.
- All three are FORCE ROW LEVEL SECURITY with the tenant policy; `foyer_radius` has no
  grant on any of them.

## Operator settings (migrations 00036, 00037)

The admin-configurability rule: every operator setting is set in the console,
stored here, and in force without a restart.

- **`maintenance_settings`** — one row per tenant, or none (the defaults). The bounds
  and defaults are `MaintenanceLimits` in `maintenance.go`, stated once; the CHECKs
  repeat them as a backstop and `Set` refuses first with `ErrMaintenanceOutOfRange`.
  `stale_after_missed_updates` is at least 3 because the console marks a session
  orphaned at 2. `SweepRetention` takes the batch through `SweepTargets.BatchSize`
  (carried to every category's DELETE loop by `WithSweepBatch`); a caller that sets
  none gets the default.
- **`guest_wifi_keys`** — the guest SSID's WPA passphrase, one per site, sealed
  (`SecretContext("guest_wifi_keys", "passphrase", site_id)`). `Status` never selects
  it; `Reveal` is the voucher slip's read and has that one caller. `Set` refuses
  anything that is not 8–63 printable ASCII (`ErrNotAWifiPassphrase`). `foyer_radius`
  has no grant.
- **An account's two verifiers change in one transaction** (`SetCredentials`,
  `CreateWithCredentials`): a refused 802.1X opt-in — the global username taken —
  leaves the argon2id verifier as it was, and an account created opted in is not
  created when the opt-in is refused.

## The console's first-access setup (migration 00040)

- **`console_setup`** — one row per tenant: `code_digest` is the SHA-256 of the
  installer's setup code, NULL exactly when the setup is complete (a CHECK keeps the
  two columns from disagreeing). The code itself is never stored. `Open` refuses
  (`ErrSetupNotNeeded`) where an enabled administrator exists, checked under the row
  lock; `Complete` locks, compares, hashes (only after a match), inserts the
  administrator and closes the row in one transaction. `HasAdministrator` counts
  **enabled** administrators outside a transaction, so the console can name the one
  state in which it has nobody to sign in as. `foyer_app` holds SELECT,
  INSERT and UPDATE.

## The RFC 8908 address book (migration 00045)

- **`portal_addresses`** — one row per (tenant, client address): the session, the MAC and
  the grant last seen from it, `seen_at`, and `ambiguous_until`. `PortalAddressRepo`
  restates `portal.MemoryAddressBook`'s rules as one statement each (a dead entry is
  replaced whole; a second MAC marks it ambiguous for a window and drops its grant; the
  same MAC keeps its grant across sessions), so two processes writing one address agree.
- **Personal data, registered**: `client_mac` identifies a MAC subject (eraser at Order
  15, before the grants), `address` goes with its row, and `grant_id` cascades from
  `portal_grants`, so an account's erasure takes its entries with its grants. The
  portal-session job reaps entries a window (a day) after they were last seen.

## The trusted-device key ring (migration 00053)

- **`device_cookie_keys`** — one row per key of the console's trusted-device cookie
  ring: a 16-hex-digit id, the 32-byte key **sealed** under FOYER_KEK
  (`SecretContext("device_cookie_keys", "key_sealed", id)`), and `created_at`.
  `foyer_app` holds SELECT, INSERT and DELETE — a key is never rewritten.
- **`DeviceCookieKeyRepo.Ring` is the one reader and the one writer**, and it is one
  transaction under a transaction-scoped advisory lock, so two processes starting
  together mint one key: it drops every key whose successor has been active for
  `keepFor`, mints when there is no usable key or the newest is `rotateAfter` old,
  and answers the usable keys newest first. **A row that does not unseal is skipped,
  never fatal and never deleted** (its KEK may return), and if that leaves nothing a
  key is minted — the fail-safe direction is that devices become untrusted once.
  `TestTheDeviceCookieKeyRingSurvivesARestartSealed`,
  `TestADeviceCookieKeyUnderAnAbsentKEKIsReplacedNotFatal`.
- Not personal data: a random key naming nobody; nothing to register for erasure.

## The retention half of an erasure is an interface

- **`Erase(ctx, app, ret RetentionEraser, s)`**: the retention side is whatever
  performs `EraseRetained` — `*Retention` in-process, `internal/jobs.RemoteClient`
  when the maintenance jobs run in their own container. The erasers are this
  package's registry either way, resolved on the side that runs them.
  `(*Retention)(nil).EraseRetained` is an error, never a panic.
