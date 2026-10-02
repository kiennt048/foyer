# Database rules

> Governs `internal/store` — the schema, migrations, roles and row-level security.

## Migrations, not a schema file

`internal/store/migrations/*.sql`, embedded, applied by `foyerctl migrate`. There is
no `schema.sql` and there must not be one again: a single file applied by the
Postgres entrypoint cannot upgrade anybody, and [`docs/architecture.md`](../architecture.md)
§12 makes the upgrade path part of the first release precisely because *the first
person to install must be able to upgrade*.

**An applied migration is immutable.** Fix a mistake with a new migration. The lab gets a
one-shot `migrate` service that the backend and FreeRADIUS gate on with
`service_completed_successfully` — separate, because `cmd/foyer` refuses to start
without a database and therefore cannot be the thing that prepares one, and because a
failed migration should be a non-zero service rather than a line in a crash loop.

## Three nouns, and a read model that is not a fourth

`controllers` → `shared_secrets` → `radius_clients` → `access_points`. This is the
object model in [`docs/architecture.md`](../architecture.md): **"access point" is not
one noun but three** — the controller that manages devices, the RADIUS client that
sends packets, and the device a guest associates to.

The contract's `Nas` is a **read model** — `radius_clients LEFT JOIN access_points`,
one row per device. Twelve APs behind one `clients.conf` row are twelve devices; a
MikroTik with no AP is one. `Nas.RadiusClient.ID` always reaches the `clients.conf`
row, because **rotating a secret acts on the client, not on the device.**

`Capabilities` lives only on `radius_clients`. A copy on the controller would be a
second source of truth for a value that decides whether a control exists at all.

### `nas` is a projection, `radius_clients` is the system of record

FreeRADIUS reads the `nas` table with `read_clients = yes`, and it holds **the one
declared cleartext copy of a shared secret**, because FreeRADIUS validates
`Message-Authenticator` itself and cannot use an encrypted one. Everything else keeps
the secret enveloped (below). Write the projection from `radius_clients`; never let
an operator edit `nas` directly, and never treat it as authoritative.

## `radacct` and `radpostauth` are FreeRADIUS's, and the database enforces it

Go never writes them. The stock column set is kept **verbatim** — it is the project's
exit hatch, since the data stays readable by stock FreeRADIUS if Foyer disappears.
Indexes, partitioning and retention are ours; the column set is not.

The rule is enforced by **PostgreSQL roles**, not by convention: `foyer_app` has
`SELECT` only. There are **two** audited exceptions, each with a role of its own:

- the stale-session reaper, `foyer_reaper`, with a **column-level** `UPDATE` grant four
  columns wide;
- retention and data-subject erasure, `foyer_retention` (migration `00020`), with
  `SELECT` and **`DELETE` only** — on `radacct`, `radpostauth`, and the two tables Foyer
  made append-only for the application (`audit_log`, `authorization_events`), plus
  column-level `SELECT` on the venue-resolving columns of `radius_clients` and
  `access_points` so an erasure by account stays inside the account's venue. All of its
  SQL is in `internal/store/retention.go`, the one file allowed to name a `DELETE` on the
  FreeRADIUS tables (a CI check enforces this), and `OpenRetention` refuses a connection
  that could also insert, rewrite or truncate them. This exception exists so that a
  retention period and a data subject's erasure can be honoured; widening it — an
  `UPDATE` to pseudonymise in place, a second file — is a new design decision, not an
  edit.

A row-level-security table with no `DELETE` policy answers a `DELETE` with *zero rows
affected* and no error. `audit_log` has no such policy for the application, on purpose;
00020 adds one scoped `TO foyer_retention` alone, and without it the audit sweep would
report success and remove nothing.

**An erasure pseudonymises the audit log; nothing deletes one entry** (migration 00052,
which does this without widening `foyer_retention`). The only UPDATE path on
`audit_log` is the SECURITY DEFINER function `foyer_audit_pseudonymise`, owned by the
NOLOGIN role `foyer_audit_redactor` (SELECT, and UPDATE of `subject_id`,
`subject_name`, `params`), with an UPDATE policy scoped to that role. `foyer_app` has
EXECUTE on the function and still **no UPDATE** on the table. The function only
replaces a MAC-shaped match or an exact identifier on guest entries with `pseudonym:` +
16 hex (an HMAC under a subkey of `FOYER_KEK`). A new SECURITY DEFINER function follows
the same shape — a non-superuser owner holding only what it needs, `search_path`
pinned, EXECUTE revoked from PUBLIC — or `TestNoSuperuserOwnedFunctionRunsForALoginRole`
is red.

**Every retention floor is a CHECK** (00051): `retention_policies_floor` against
`foyer_retention_floor(category)`, the same figures as `RetentionCategory.MinDays`. A
new category adds its floor to that function in its migration;
`TestTheDatabaseHoldsEveryRetentionFloor` fails otherwise.

**Completeness of an erasure is tested against the schema**, not the registry:
`erasurecoverage_test.go` scans every text-like, inet, macaddr and JSON column of every
table and view for the erased identifier. A column that stores a guest identifier
anywhere — including inside a JSON document — is erased or pseudonymised by the
subject's erasure, or that test is red.

### A permission test must name the object it protects

`radacct.radacctid` is a `bigserial`, so an `INSERT` calls `nextval()`, which needs
sequence `USAGE` as well as table `INSERT`. PostgreSQL answers `42501` for either
missing grant, so a test asserting on the SQLSTATE alone cannot tell **the table is
protected** from **the table is open and the sequence refused**.

- **Assert that the refusal names the object you meant.** `permission denied for
  sequence radacct_radacctid_seq` is not evidence that `radacct` is protected. The
  helper requires the message to name the table.
- **A negative test whose subject has more than one way to fail is a test of
  whichever guard fires first.** Remove one guard at a time and confirm the test still
  fails for the reason it names ([`testing.md`](testing.md)).

### `nas` has no row-level policy, and that is deliberate

Every other tenant-scoped table in this schema carries an RLS policy. **`nas`
does not, and adding one would stop the server.**

FreeRADIUS reads the client list itself, over `rlm_sql`, and it never sets the tenant
GUC — it has no concept of one. An unset GUC matches no row under a policy, so a
policy here yields a server that loads **no clients** and therefore discards **every
packet** as coming from an unknown NAS. The failure is total and it looks like a
network problem.

The absence is a real cost in every other respect, and it pays for itself in one
place: it is why the projection can name **which other tenant** already holds a
conflicting address, in an error an operator can act on. `nasname` is globally unique
because FreeRADIUS's client list is one flat namespace, and it is the value the server
matches on, so it can be neither prefixed nor synthesised.

**If you are here to add the missing policy, this section is why you should not.**

### A partial unique index is not the guarantee it looks like

`portal_grants` enforces single-use vouchers with
`CREATE UNIQUE INDEX … ON portal_grants (voucher_id) WHERE voucher_id IS NOT NULL`.

**PostgreSQL treats NULLs as distinct in a unique index by default.** So a total
unique index on a nullable column already permits unlimited NULL rows, and the
`WHERE voucher_id IS NOT NULL` predicate buys **index size, not a constraint**. The
two forms are behaviourally identical for every row that matters; `NULLS NOT
DISTINCT` is what makes them differ.

- **Say what a partial index is for.** If the predicate is there for size, write that;
  if it is there for correctness, it probably is not doing what you think.
- **A sabotage that stays green is a finding.** It says the test is not measuring what
  its name claims.

The single-use guarantee has two layers and the test names which held: the claim
statement (`UPDATE … WHERE state = 'unused' … RETURNING`, one statement, per
[`security.md`](security.md) §9) and the index. If the claim is relaxed to
`state IN ('unused','in-use')` the index still allows only **one** grant row, but the
losing guest is told `generic` instead of `already-used`. Two layers fail differently,
and a test that cannot tell them apart is testing one of them.

### Never restate a constraint in code that talks to the database

A test harness, a seeder or a validator that re-implements a `UNIQUE` rule in its own
language is a second source of truth for a fact the database already owns, and it
drifts silently the day the constraint changes — **still passing, still printing a
reason, and now describing an index that no longer says that.**

The shape that does not drift: **attempt the write, and let the database refuse it.**
Record the refusal under the constraint the server itself named — matching on the
SQLSTATE *and* the object name, because two objects can answer the same SQLSTATE.

## Multi-tenancy from migration 00001

`tenants`, `foyer_current_tenant()`, and RLS policies exist from the first migration
even though v1 ships single-tenant. [`docs/architecture.md`](../architecture.md)
§8.10: retrofitting this rewrites every query in the codebase. Adding a table means
adding its policy in the same migration — **and `FORCE ROW LEVEL SECURITY` on it**, or
`TestEveryRowLevelPolicyIsForced` goes red.

## Who logs in: the login is the privilege role

`foyer_app` and `foyer_radius` are LOGIN roles (migration `00019`) and the product
logs in as them, never as the schema owner. **`SET ROLE` is not a boundary against the
connection's own SQL: `RESET ROLE` undoes it from any statement**, so a connection
that logs in as the owner and then sets a role is one injection away from the owner.
Logging in as the privilege role means `RESET ROLE` lands on the same privileges.
Four rules follow:

- **`store.NewStore` refuses a login that is more than `foyer_app`.** Do not add a
  lift for the lab; the lab logs in as `foyer_app` too, which is what keeps a grant
  the application needs from being noticed only in production.
- **No password in a migration, and provisioning does not grant LOGIN.** `00019`
  grants LOGIN; `store.SetLoginPassword` (from `foyerctl migrate`) sets a SCRAM
  verifier and refuses a NOLOGIN role.
- **A test about who logs in must log in.** `storeAsRole` is `SET ROLE` from the
  owner, so it cannot see a defect in the login arrangement. `loginroles_test.go`
  uses real logins, and so does the `foyer_radius` credential-view test. Note that
  `withDefaults` turns `TenantID: ""` into the default tenant, so a test that means
  to run without the tenant GUC must not rely on an empty tenant ID.
- **`FORCE` moves the `security_invoker` trap, it does not remove it.** A
  `security_invoker = false` view runs under its OWNER's row security. Under FORCE a
  non-superuser owner is subject to the policies, sees no row without the tenant
  GUC, and the view FreeRADIUS reads returns nothing. `radius_pap_verifiers` is
  therefore owned by `foyer_credential_view` with a SELECT-only `USING (true)`
  policy for that role — not by the superuser, which would "work" by running the
  minimally-privileged view with every privilege.

## Secrets are enveloped, and the key is not in the database

RADIUS shared secrets, controller API keys and NT hashes: AES-256-GCM data key per
row, key-encryption key from `FOYER_KEK`, outside the database. An NT hash is
**password-equivalent** — it is the sole input to the MSCHAPv2 response, so a stolen
hash authenticates directly. It lives in `account_nt_hashes`, never in the accounts
table, and only for identities explicitly opted in to PEAP-MSCHAPv2. See
[`security.md`](security.md) §1.

The `nas` projection's cleartext secret is the one exception and is declared above.
If you find a second, it is a defect.

## Discovery is idempotent, and a vanished device is not a deleted one

Access-point ids derive from the MAC, so rediscovery updates rather than duplicates.
An AP that stops appearing is marked `missing`; **it is not deleted**, because its MAC
is what resolves last week's sessions. A session row that can no longer name the
device it happened on is a support call nobody can answer.

## `compat.go` is an adapter, not an API to extend

It exposes a small set of loose functions, spelled in terms of the repositories, for
callers in `internal/aaa`, `internal/api` and `cmd/foyerctl`. Where a signature cannot
express something the schema knows (a site, in particular) the function says so in
place. **Do not add to it and do not build on it** — new callers use the repositories
directly.
