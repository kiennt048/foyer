# internal/apistore — CONTRACT

The adapter between `internal/api`'s ports and `internal/store`'s repositories.
Without it, `api.NewServer` returns a server with no ports wired and every
operation answers `501`.

## Invariants

1. **It belongs to neither side, and imports both.** `internal/api` must not import
   `internal/store` — that is the coupling `ports.go` exists to prevent — and
   `internal/store` must not import the contract's generated types. A third package
   is what keeps both true. Nothing else in `internal/` may import this one; the
   composition root is `cmd/foyer`.

2. **No behaviour that either side could have carried.** Where a repository answers
   a port, it is called directly. Where it cannot, the gap is stated at the call
   site and reported — never widened into a second implementation of something the
   store should own.

3. **`internal/store/compat.go` is not used.** It is a small adapter of loose
   functions for older callers, some of them lossy and saying so in place. This
   package builds on the repositories.

4. **Only `api.ErrNotFound` and `api.ErrConflict` carry meaning across the seam.**
   Everything else becomes `/problems/internal` with no text, so a store error
   cannot reach a client carrying a table name or a constraint. `out` is the single
   translation point.

5. **A secret is written and never read back.** `RegisterAccessPoint` and
   `StoreRotatedSecret` are the only methods that touch shared-secret material, and
   neither has a counterpart that reveals one. Controller credentials are handed to
   the repository as bytes and sealed there, so no path in this package can store
   one unsealed.

6. **This package never sees a stored password.** `Authenticate` and
   `ChangePassword` verify an argon2id verifier through `internal/identity` and
   return an operator or nothing. There is no method that returns a hash, and the
   unknown-username branch pays for a verification so that timing does not become
   the username oracle the identical error message removes — **on the portal's
   `AuthenticateGuest` too** (`TestAnUnknownGuestUsernamePaysTheSameVerification`
   counts the decoy).

6a. **The first-access setup completes in one transaction** (`CompleteSetup`,
   `store.ConsoleSetupRepo.Complete`): the setup row is locked, the code compared,
   and only then is the password hashed — a wrong code costs a SHA-256, never an
   argon2id — the administrator inserted and the setup closed. Two racing callers get
   one administrator and one `ErrSetupClosed` (`TestTheSetupWorksOnceAndOnlyWithItsCode`).

6b. **A closed setup and an install nobody administers are two answers**
   (`HasAdministrator`). `IsOpen` alone cannot tell a finished install from a
   bootstrap that never ran, and the console would show the latter a sign-in form it
   could never satisfy. It counts **enabled** administrators, the population `Open`
   gates on, outside the transaction
   (`TestAFreshInstallWhoseBootstrapNeverRanSaysSo`).

7. **`radacct` and `radpostauth` are read-only.** `SessionLog` and `AuthAttemptLog`
   have no write method, and the database agrees: the application role holds
   `SELECT` and nothing else on both tables.

7a. **A disconnect writes no accounting row either.** `EndSession` asks the driver;
    the device's own Acct-Stop is what closes the `radacct` row, and
    `TestDisconnectSendsAVerifiableDisconnectRequest` asserts `acctstoptime` is still
    null after an ACK. `CanEndSession` and `EndSession` resolve the driver through one
    function, `resolveSessionTarget`, so the question and the act cannot be answered
    by two different drivers; `CanEndSession` reads the **driver's**
    `CanDeauthGuest`, never the stored column (invariant 10). The withdrawals
    (`RevokeOpenGrantsForMAC`, `Vouchers().Revoke`) are one statement each.

8. **Every list goes through `fill`, including the ones that need no filtering.**
   One cursor encoding per operation, so a query that gains a filter does not hand
   the client an incompatible cursor for the same operation.

9. **The `ChangeClass` is not decided here.** For the client-table sync it is
   `raddb.ClassifySync`'s answer against the startup snapshot (`network.go`). For a
   shared-secret rotation, `ChangeClassifier` is declared in this package and
   answered conservatively — `restart` for every driver — because `internal/raddb`
   does not export a per-driver classification for rotation, and `restart` is the
   only safe direction for a value whose job is to warn.

10. **A capability the console *stored* is a claim; the driver is the fact, and an
    action that needs both requires both.** `SecretRotation` reads
    `radius_clients.capabilities` — a written column — and the driver's computed
    capability, and answers `canPush` only when they agree. Reading the column alone
    is not enough: `PushSecret`'s premise is that no driver implements it, so a row
    claiming otherwise would send the operator into a `500` *after* the secret had
    been generated and stored. Where the two disagree the answer is **manual**, never
    a refusal — the narrow reading.

11. **The guest surface is its own value, not the same adapter fourteen times.**
    `PortalPorts()` returns all **seven** ports or none: `api.New` refuses a
    half-wired portal because a grant store with no driver seam is a portal
    recording grants for devices nothing confirmed. There is no method here that
    takes a MAC and returns a session, because `portal.SessionStore` declares none
    and the schema has no index that would make one cheap.

11a. **`Authorizations` is the seventh.** A nil port gets `portal.NewService`'s
    log-only recorder, which reaches one reader out of three — it cannot write
    `listAuthAttempts`, so the console shows an operator nothing, and it cannot set
    `Grant.Delivery`, so the guest whose controller refused still reads *You're
    connected*. The contract carries the reader, and production must supply the
    writer.

    `Record` writes both readers because they are two readers of one fact, and the
    transaction is the repository's. This package's only job at that seam is to keep
    both halves inside the one call that owns them — and to drop a `status` where
    nothing answered, because a number beside `unreachable` was invented from a
    transport error.

12. **The rate limiter belongs to the adapter.** `cmd/foyer` builds two
    `portal.Service` values over one seam — the API server's, and the front door's
    `EntryHandler` — and two limiters would be two budgets for one guest. It is
    in-process, which is a stated limit: a second replica gets twice the configured
    rate, and `portal.Limiter` is a port so the replacement is a shared counter
    rather than an edit to a handler.

13. **`Plan.EnforcedBy` is dropped on the way in and restored on the way out of a
    grant.** The value on a batch or an account was decided when that row was
    created; a grant's is derived at grant time from the driver that will actually
    be asked to honour the plan. Reading a *grant back* is the one direction where
    the stored value is the answer, and dropping it there would report `unenforced`
    beside a plan with four limits in it — the portal telling a guest nothing was
    capping them while the NAS was.

14. **An exported row goes through the converter the list uses** (`reports.go`):
    `sessionOut`, `voucherOut`, and `grantOut` — the last extracted from
    `portalPorts.Grant` so the grant export and the portal read one conversion.
    A trend bucket's `bytesFreshness` is `onlineFreshness` over the bucket's oldest
    open sample, so the chart and the `clientsOnline` card answer by one rule with
    one 300-second floor.

## The two seams that are per-NAS rather than per-controller

**`NASDrivers` is a second interface, not a method on `Drivers`.** The CoA family has
no controller to build a driver from — `internal/nas`: *"there is nothing to discover;
the device is its own controller"* — and the contract agrees, since `ControllerVendor`
excludes `mikrotik` and `coa` while `NasDriverKind` has both. `Drivers` is implemented
outside this package (`tools/codegen/differ` and two harnesses), so widening it would
break builds for a capability none of them has an opinion about. A `Drivers` that also
implements `NASDrivers` serves both; one that does not gets the real vendor drivers on
the per-NAS half.

`ForNAS` builds MikroTik and the CoA family, and refuses the rest with different
refusals because they are different people's problems: a controller vendor is
`ErrNoNASDriver` (driven through its controller), and a device with no shared secret is
refused **before** a driver exists (an empty secret produces packets the NAS drops,
which reads as *unreachable*).

**`driver: coa` resolves through `radius_clients.coa_vendor`** (migration 00015). `coa`
is one enum member for Cisco and Aruba; they identify a session differently, and
RFC 5176 §2.3 makes the NAS match on every session-identification attribute in the
request, so the wrong dialect matches nothing — a careful device answers
`Error-Cause 503` and a careless one **ignores the request and ACKs it**. The lab cannot
produce that case.

So `coaVendorFor` has **no default branch and must never grow one.** NULL is *nobody
has said* and stays representable on purpose, because the alternative shape — splitting
the driver enum — would have made the migration guess a vendor for every existing row.
An unset or unrecognised value is `ErrCoAVendorUnknown` **naming the device**, and there
are two gates for the one fact: `createNas` refuses a new row that names no vendor
(`required` on `coaVendor`), and this refuses every row that gate cannot reach.
`TestAnUnsetCoAVendorNeverReachesADriver` is the lock; it fails against a
`coaVendorFor` that defaults to Cisco.

**Nothing here is evidence about Cisco or Aruba hardware.** Every capability of both
stays UNVERIFIED in `internal/nas`; only the wire encodings are DOCUMENTED, from
FreeRADIUS's own dictionaries. This decides **which** unverified driver is built.

### The callers

`probeNas`, `rotateNasSecret` and the session-end path resolve **whichever seam the
device is behind** — `deviceDriver` for the first two, `NASDriverFor` directly for the
third. A seam that only resolved a controller first, and returned when there was none,
would be a library rather than a feature.

**A device with a controller must never reach the per-NAS seam.** A UniFi access point
is driven through the controller that adopted it, and a CoA driver built from its
`radius_clients` row would aim RFC 5176 at a radio with no CoA listener and a secret
nobody typed into it. `deviceDriver` prefers the controller and `ForNAS` refuses the
controller vendors from the other side, so the two cannot both answer for one device;
`TestAControllerDeviceNeverReachesThePerNASSeam` asserts it, with the positive beside
the negative because *zero calls* is equally true of a seam nothing calls.

`probeDirectly` consults the probe's result where the controller path deliberately
ignores it: on the controller path `probeController` diagnoses the device properly and
this only wants the warnings, while on this family there is no second probe. That probe
is a **write** — a Disconnect naming a session id that cannot exist — because RFC 5176
has no read in it and the alternative is a probe that measures nothing and says `ok`.

**`Adapter` is the `nas.SessionSource`.** RFC 5176 has no query in it, so a CoA driver
has no presence endpoint and `docs/rules/security.md` §4 still requires presence before
a grant; the evidence available instead is Foyer's own accounting. `ActiveSession` keeps
the three-way answer `aaa.PolicyStore` insists on — `(zero, false, nil)` is *no such
session*, any error is *I could not look* — because a caller that folds them together
turns a database outage into *your device is not on the network*.

`NASIdentifier` and `AuditSessionID` come back empty, and that is the table rather than
this package: the stock `radacct` column set has neither. They are deliberately not
filled from `radius_clients`, because the driver compares them against its own
configuration read from the same row — a check comparing a value against a copy of
itself.

## Site scoping is a security property

Every site-scoped port narrows on the site, including the two surfaces whose
repository cannot: `listAuthAttempts`, where `radpostauth` carries no NAS address
and no tenant and the scope is resolved from `Called-Station-Id` through
`access_points`; and `listSessions`, where the multi-state and device filters are
applied after the read.

A row that cannot be resolved to a site is **excluded whenever a site is asked
for** and included when one is not. `scoping_test.go` asserts both halves, and
re-injects the naive implementation to prove the assertion can fail.

## Two vocabularies for the walled garden, and they agree on one word

`nas.WalledGardenType` is `fqdn | ip | none`; `gen.GardenKind` is
`fqdn-sni | ip-range | none`. Only `none` is spelled the same, so a driver value
crossing into the contract goes through `gardenKindOut` and never through a
conversion. A bare cast emits `fqdn` — a value `GardenKind.Valid()` rejects and the
console's three-way switch does not know.

A cast survives tests only while both producers agree by accident: the only shipped
driver returns the one shared word, and a fake can return the wrong spelling of a
capability no deployment has. That agreement is the bug.

## Erasure runs on a second connection, and says so when it cannot

`EraseDataSubject` resolves the subject (`store.MACSubject`, `Store.AccountSubject`,
`store.PhoneSubject` — a number that is not one is `api.ErrNotAPhoneNumber`)
and hands it to `store.Erase`, which runs the FreeRADIUS-side erasers on the
`foyer_retention` connection (`Config.Retention`) and Foyer's own in one application
transaction. **With no retention connection the operation refuses** --
`/problems/internal`, and the reason in the log -- rather than erasing half a person and
answering 200. The receipt lists every table an eraser covers for the subject's kind,
zeros included; the audit entry the API writes names the subject masked
(`Subject.Masked`). **The entry is inserted by `store.EraseRecorded` in the application
transaction** (`storeAuditEntry`, the same id and time Append would give it), so an
entry PostgreSQL refuses rolls Foyer's side back and the operation fails; a
failure is logged at ERROR with the subject masked, because the FreeRADIUS side has
already committed. `store.EmailSubject` resolves the `email` kind
(`api.ErrNotAnEmailAddress`), and the receipt's `pseudonymised` is the store report's
audit-log count. `AmendRetentionPolicy` does the same through
`PrivacyRepo.SetRetentionRecorded`. `RetentionPolicy` refuses a store category the contract's closed
`RetentionCategoryId` does not name (`TestTheContractsRetentionCategoriesAreTheStoresRegistry`
holds the two together both ways).

## What this package will not do

- Answer a port with a plausible value when the store cannot supply one. A gap is
  `not-measured`, an empty list, or a named error.
- Add a method to `internal/store/compat.go`, or build on one.
- Compute a `ChangeClass`, a capability, or a probe result. Those are measurements,
  and this package only ever renders them. (The client-table sync's class is
  `raddb.ClassifySync`'s answer against the startup snapshot, rendered.)
- Write `radacct` or `radpostauth`. **The `nas` projection has exactly one writer
  here**: `Network.SyncRadiusClients`, which calls `store.NasProjectionRepo.Sync` —
  the same one transaction `foyerctl sync-nas` runs — after every RADIUS-client
  write and at start, because an operator never runs a CLI and a client added in the
  console must reach FreeRADIUS. Nothing else in this package writes it.

## The network tag

`Network` (network.go) is `api.NetworkSettingsStore`: the portal's address,
throttles and ceilings, the two certificates, FreeRADIUS's generator settings, the
per-client CoA timing, and the last sync and apply (migration 00030).

- **Private keys are sealed by the store and unsealed for one reader each**: the
  portal certificate by `PortalCertificate` (the listener serves it), the EAP-TLS
  key by `generatorSettings` (the tree writer puts it in the generated tree). No
  other method returns a key, and `EapCertificate` never does.
- **A sync failure is an answer, recorded and audited, never an error to the write
  that triggered it.** A cross-tenant address conflict and a missing KEK are
  `refused`; anything else is `failed` and its text goes to the log only.
- **The restart mechanism ships with Foyer and is never a string, stored or
  typed.** `RadiusApplier` is `cmd/foyer`'s, over `raddb.Handoff` and the supervisor
  in the FreeRADIUS image. With none, an apply answers `not-configured`; with a tree
  writer and no supervisor announced, `restart-unavailable`; otherwise the
  supervisor's own answer — `applied` (with `restarted`) or `failed` with its
  sanitised `detail`, and a supervisor that never answers is `failed`, never
  `applied`. `RecordStartupApply` records the control plane's start-up hand-over the
  same way, so a stored EAP certificate FreeRADIUS refuses at start is shown.
- **CoA timing reaches the driver per build.** `NASDriverFor` reads the stored
  timing and hands it to a `TimedNASDrivers`; a `NASDrivers` that is not one builds
  with the defaults (`nas.DefaultCoATimeout`, `nas.DefaultCoARetries`).
- **The trusted-device key ring is persisted.** `Ports().DeviceKeys` is the
  adapter, over `store.DeviceCookieKeyRepo.Ring`, so a restarted process keeps
  every console device trusted. `TestATrustedDeviceSurvivesARestartOverPostgres`
  replaces store, adapter and server and keeps only the database and the KEK.
