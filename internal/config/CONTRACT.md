# internal/config — CONTRACT

What the control plane reads from its environment, the startup assertions it refuses
to start without, and the certificate checks the console and the portal listeners
share. It imports nothing from this repository; `internal/store` imports it, so it
must never import `internal/store` (which is why `RadiusDatabaseUser` spells
`store.RadiusRole` again — §7).

---

## Which settings the environment owns, and which the console does

The admin-configurability rule: an operator configures nothing outside the admin
console, and an env var is not an operator interface. So
every variable below is one of three kinds, and the kind is what the code does, not
what the variable's name suggests.

**Console-owned — the env var is a bootstrap fallback and the database value wins.**

| Variable | Console operation | Where the override happens |
|---|---|---|
| `FOYER_PORTAL_URL` (or derived, §3) | `updateNetworkSettings` | `api.Server.ReloadNetwork`: a stored `portalUrl` replaces `NetworkBootstrap.PortalURL`, source `console` |
| `FOYER_VENUE_URL` | `updateNetworkSettings` | same; source `console` over `environment` |
| `FOYER_PORTAL_TLS_CERT` / `_KEY` | `putPortalCertificate` | `cmd/foyer` `portalRuntime.served()`, per handshake: the console's pair when servable at the address in force, else the files, else none |

`cmd/foyer` hands the first two to `api.New` as `NetworkBootstrap`; they are what the
console reports as `source: environment` (or `derived`) until something is saved.

**Install-time — written by `deploy/`, never by an operator.** `FOYER_DATABASE_URL`,
`FOYER_KEK`, `FOYER_POSTGRES_PASSWORD`, `FOYER_REAPER_DATABASE_URL`,
`FOYER_RETENTION_DATABASE_URL` (the secrets, §2); the four listener addresses
`FOYER_PORTAL_ADDR`, `FOYER_PORTAL_TLS_ADDR`, `FOYER_ADMIN_ADDR`, `FOYER_POLICY_ADDR`;
the Policy API's mTLS material `FOYER_POLICY_TLS_CERT`, `_KEY`,
`FOYER_POLICY_CLIENT_CA`; the console's `FOYER_ADMIN_TLS_CERT`, `_KEY` (paths the
process writes a self-signed pair to) and `FOYER_ADMIN_TLS_NAMES` (computed from the
host by `deploy/`); and the FreeRADIUS handoff `FOYER_RADDB_TARGET`,
`FOYER_RADDB_POLICY_CLIENT_CERT`, `_CLIENT_KEY`, `_SERVER_CA` and `FOYER_POLICY_HOST`.
`deploy/compose.yaml` sets every one of these; `first-boot.sh` generates the secrets.
`GOMEMLIMIT` is the Go runtime's own variable, set by compose; `Config` records only
whether it is present (`GoMemLimitSet`).

**Lab-only.** `FOYER_LAB_INSECURE`, §4.

## Invariants

### 0. `Config` is the whole of what the control plane reads from its environment

`cmd/foyer` reads no variable of its own — `TestTheControlPlaneReadsTheEnvironmentOnlyThroughConfig`
parses its sources and fails on `os.Getenv`, `os.LookupEnv`, `os.Environ`,
`config.EnvOrFile`, `store.KeyringFromEnv` or `store.NewStore` there. It opens the
store with `store.NewStoreWithKeyring` and a keyring built from `Config.KEK`, so
`FOYER_KEK` is read once. The one other reader of the environment is the Go runtime,
which reads `GOMEMLIMIT` itself.

### 1. `Load` never fails; `Validate` is the assertion, and runs before anything is opened

`Load` has no error return. A secret it could not read travels in an unexported
field and `Validate` reports it **first**, so an unreadable `FOYER_KEK_FILE` is never
mistaken for the permitted absence of a key. `cmd/foyer` calls `Validate` before it
opens the database or binds a port, and logs every `Warnings()` line at start.

**The maintenance role URLs are the exception, deliberately.** `ReaperDatabase` and
`RetentionDatabase` are `SecretValue{Value, Err}`; their read errors are not
startup refusals, because this process also answers every Wi-Fi sign-in. `cmd/foyer`'s
`openMaintenance` carries the error into the job, which fails every cycle and says so.

There is **no default database URL**. An empty `FOYER_DATABASE_URL` is a refusal.

### 2. Secrets may arrive as `NAME_FILE`, and `EnvOrFile` is the one reader

`EnvOrFile(name)` reads `NAME`, or the file `NAME_FILE` names (PostgreSQL's
`POSTGRES_PASSWORD_FILE` shape). Three rules, each pinned in `secretfile_test.go`:

- **Both set is a refusal**, never a precedence rule.
- **Only a trailing `\r`/`\n` is trimmed.** Any other whitespace is part of the secret.
- **An unreadable file is an error naming the path, never the contents.**

`Load` reads `FOYER_DATABASE_URL`, `FOYER_KEK`, `FOYER_REAPER_DATABASE_URL` and
`FOYER_RETENTION_DATABASE_URL` this way, and `FOYER_POSTGRES_PASSWORD` **only when
`FOYER_RADDB_TARGET` is set** — a control plane that writes no FreeRADIUS tree does
not hold the data plane's password. Every other variable is a plain `os.Getenv` with
no `_FILE` form; the TLS ones are paths, not key material.

### 3. The portal URL is derived only for a plain-HTTP portal, and the venue URL never

With no `FOYER_PORTAL_URL` and no portal TLS pair, `Load` derives
`http://<portal bind>/en/`, turning a wildcard host into `127.0.0.1` (right for a
developer, visibly wrong for a deployment), and sets `PortalURLDerived`.
`FOYER_VENUE_URL` is never derived. Both must pass `httpOrigin`: absolute
`http`/`https`, a host, no userinfo — `CheckWebAddress` exports the same rule for the
console's writes.

**With a portal TLS pair set, `FOYER_PORTAL_URL` must be `https://` with a host**, and
`FOYER_VENUE_URL`, if set, `https://` too — even though the console can later replace
the address. This is right, not a leftover: `Validate` runs before the database is
opened, so no console value exists yet; the files' pair is checked at start against
this URL's host (`cmd/foyer` `newPortalRuntime`), and until `ReloadNetwork` reads the
console's settings the plain-HTTP listener redirects guests to this origin. A
certificate with no https address to serve it at has nothing to be checked against
and nowhere to send anybody. Once the console saves an address, that address is the
one every later check uses (§8).

### 4. The lab acknowledgement is one exact sentence and lifts exactly two refusals

`FOYER_LAB_INSECURE` counts only when it equals `LabAcknowledgement`
(`i-am-a-lab-and-not-a-deployment`); any other value is off. It lifts:

- a `FOYER_DATABASE_URL` containing a `knownDefaults` entry (checked on every start,
  case-insensitively, not behind an environment name);
- a plaintext Policy API on a non-loopback address.

It does **not** lift TLS-without-client-CA on the Policy API. It is a warning at every
start.

### 5. The Policy API boundary has one rule, `PolicyBoundaryError`

Called by `Validate` and by `foyerctl doctor`, so the diagnostic cannot disagree
with the startup assertion. Refuses: a policy or console TLS pair half-set; a
non-loopback Policy API with no TLS (unless §4); a non-loopback Policy API with TLS
and no `FOYER_POLICY_CLIENT_CA`. Loopback without a client CA is permitted and is a
warning. `LoopbackOnly` treats a wildcard bind as reachable.

### 6. Three listeners are three listeners

`Validate` refuses any two of portal, admin and policy — plus the HTTPS portal
address, **only when a portal pair is configured** — sharing a port on the same host
or with either a wildcard. Defaults: portal `0.0.0.0:8081`, HTTPS portal
`0.0.0.0:8444`, admin `0.0.0.0:8443` (reachable by default,
`TestTheConsoleIsReachableByDefault`), policy `127.0.0.1:8080`.

### 7. The FreeRADIUS database login is FreeRADIUS's own

`Postgres()` takes **where** from `FOYER_DATABASE_URL` (host, port defaulting to 5432,
database, scheme `postgres`/`postgresql`) and **who** from `RadiusDatabaseUser`
(`foyer_radius`) with `FOYER_POSTGRES_PASSWORD`. It never reuses the control plane's
login, and an empty password is an error, not a default. `RadiusDatabaseUser` must
equal `store.RadiusRole`; `cmd/foyer`'s `TestTheRadiusUserIsTheStoresRadiusRole`
holds them together. `FOYER_POLICY_HOST` (`PolicyHost`) is the host:port the tree
tells FreeRADIUS to call; empty derives it from `PolicyAddr`.

### 8. One certificate check, for an upload, a certificate file and a handshake (`portalcert.go`)

- `ParseCertificateChain` — every check that does not read the clock, first failure
  wins: both inputs PEM of the right kind, the key the leaf's, each certificate signed
  by the next. No trust-store check: a self-signed portal certificate is the
  operator's choice.
- `CheckValidAt` — the one clock rule: `not-yet-valid` before any certificate's
  NotBefore, `expired` after its NotAfter.
- `ParseCertificatePair` is the two together; `internal/api` calls it for
  `putPortalCertificate` and `putEapCertificate`, so an upload is refused on any of them.
- `CoversHost` / `CheckPortalPair` — the portal address is `https` and the leaf covers
  its host (`VerifyHostname`, IP literals against IP SANs).

Who applies which, all from this file:

| | chain | name | clock |
|---|---|---|---|
| console upload (`internal/api`) | refused | refused | refused |
| stored console pair (`servablePair`) | left out | not served | not served |
| certificate files at start (`cmd/foyer`) | **start refused** | **start refused** | loaded, not served |
| a replaced certificate file | refused, previous kept | refused, previous kept | refused, previous kept |
| each handshake (`api.NotServedReason`, leaf) | — | not served | not served |

A file whose only fault is the clock starts rather than refusing, because refusing
would take RADIUS down with the portal; it is reported `not-served` with
`not-yet-valid` or `expired` and served from the moment it is valid. The name is
checked against the portal address **in force**: `FOYER_PORTAL_URL` at start, the
console's address after one is saved.

Refusals are `*CertificateError{Field, Fault}`; `Fault` values are the contract's
`certificate-refused` reasons. **The message names neither the key nor any of its
bytes.** `CertificateInfo` carries public facts only.

### 9. A refusal never repeats a URL's userinfo

`httpOrigin`'s and `PortalTLSError`'s messages quote a URL through `shownURL`: a URL
that parses is shown without its userinfo and says so; one that does not parse, or
parses with no authority, and contains an `@` anywhere is not repeated at all.
`Postgres()` unwraps `*url.Error`, which would otherwise repeat the whole database
URL, password included. `TestARefusedURLNeverRepeatsItsUserinfo`.

## What this package does not do

- Read a certificate file. `cmd/foyer` opens the portal and console files;
  `internal/tlsca` validates `FOYER_ADMIN_TLS_NAMES`.
- Check an intermediate's validity per handshake. `api.NotServedReason` holds the
  leaf to the clock; the whole chain is held to it when uploaded and when a file is
  loaded or replaced.
- Generate anything. No default password, secret, certificate or database URL.

## The maintenance split and the named FreeRADIUS password

- **`FOYER_MAINTENANCE_URL` on the control plane is refused beside either
  maintenance login** (`MaintenanceSplitError`, part of `Validate`), and needs
  `FOYER_MAINTENANCE_TOKEN(_FILE)`. A control plane given the reaper's or the
  retention role's URL "as well" has undone the split without anything saying so.
- **`foyer maintenance` refuses to start with a KEK**, with no application login, or
  with a token under 32 characters (`MaintenanceProcessError`).
- **`FOYER_RADDB_POSTGRES_PASSWORD_ENV`** names the variable the generated tree reads
  FreeRADIUS's password from; `Postgres()` then answers `PasswordEnv` and no
  password, and refuses when `FOYER_POSTGRES_PASSWORD` is set too.
  `TestTheFreeRADIUSPasswordCanBeNamedInsteadOfHeld`,
  `TestAControlPlaneToldOfTheMaintenanceContainerHoldsNoMaintenanceLogin`,
  `TestTheMaintenanceContainerRefusesTheKEK`.
