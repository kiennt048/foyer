# Contributing to Foyer

Thank you for helping. Foyer is an authentication system, so every change is treated
as a security change until shown otherwise — the process below exists for that
reason, not for ceremony.

## Sign your commits (DCO)

Foyer uses the [Developer Certificate of Origin](https://developercertificate.org/).
There is no CLA and no copyright assignment — you keep your copyright.

Add a `Signed-off-by` line to every commit:

```bash
git commit -s -m "your message"
```

That line certifies you wrote the contribution, or otherwise have the right to
submit it under Apache-2.0. Pull requests with unsigned commits cannot be merged.

## Before you start

Read [`docs/architecture.md`](docs/architecture.md). It records *why* things are the
way they are — including several decisions that look wrong until you see the
evidence behind them.

Then read the rule file for the area you are touching, under
[`docs/rules/`](docs/rules/):

| You are changing | Read |
|---|---|
| anything under `internal/` | [`security.md`](docs/rules/security.md) |
| `internal/raddb`, `internal/aaa`, `internal/session` | [`radius.md`](docs/rules/radius.md) |
| `internal/store` (schema, migrations, roles) | [`database.md`](docs/rules/database.md) |
| `internal/nas` (drivers, vendor profiles) | [`nas-drivers.md`](docs/rules/nas-drivers.md) |
| `web/` | [`frontend.md`](docs/rules/frontend.md) |
| tests, `lab/`, `tools/` | [`testing.md`](docs/rules/testing.md) |
| `deploy/` | [`deployment.md`](docs/rules/deployment.md) |

Protocol and vendor facts — what RADIUS, the controllers and the phones actually do,
and how well each fact is evidenced — are in
[`docs/protocol-reality.md`](docs/protocol-reality.md). Each `internal/` package also
carries a `CONTRACT.md` (its invariants); read it before changing a package.

## Three rules that are not negotiable

1. **`raddb/` is generated. Never edit it.** Change `internal/raddb/`.
2. **`radacct` and `radpostauth` belong to FreeRADIUS.** Go reads them and never
   writes them, with **two** audited exceptions and no more: the stale-session
   reaper (`UPDATE`, four columns, `foyer_reaper`) and
   `internal/store/retention.go` (`DELETE` only, `foyer_retention`, migration
   `00020`) for retention periods and data-subject erasure. Never `INSERT`,
   never `TRUNCATE`, never from another file.
3. **Credential material never crosses the `rlm_rest` boundary.** See
   [`docs/rules/security.md`](docs/rules/security.md) §2 — this one is load-bearing,
   and getting it wrong turns the policy API into a credential dump endpoint.

CI and review enforce all three — the database roles themselves refuse the second.
If a check blocks you and you believe it is wrong, say so in the pull request rather
than working around it: a check being wrong is a finding worth having.

## Changes that need more than code

- **Touching an invariant?** Update the package's `CONTRACT.md` in the same change.
- **Learned something from real hardware?** Add a line to
  [`docs/protocol-reality.md`](docs/protocol-reality.md) *and* a `lab/` test, and say
  what evidence it rests on (MEASURED, DOCUMENTED or UNVERIFIED). A fix without the
  recorded fact will be rediscovered by the next person.
- **Adding a setting?** Every operator setting has a contract operation, an admin
  console screen and a database row, and takes effect without a restart. An
  environment variable, a config file or a CLI step is not an operator interface;
  secrets are write-only in the API.
- **Changing what a guest or an operator sees?** Drive it in a real browser, as that
  person would, in both languages — and for the portal, on a real phone inside a real
  captive-portal sheet. Attach screenshots to the pull request. An API call returning
  200 is not evidence the screen works.
- **Adding a vendor driver?** Inside `internal/nas` a driver and its vendor profile
  register themselves from their own files' `init()` — there is no core table to
  append to (see [`nas-drivers.md`](docs/rules/nas-drivers.md#adding-a-driver)). A
  CoA vendor is a `driver_<vendor>.go` and a `profile_<vendor>.go`; a controller-API
  vendor such as UniFi is larger, because its API needs more than one file. Making the
  vendor selectable also needs its `Vendor` constant, a `VendorForDriver` mapping and
  the contract's driver enum. **If you have to change anything else in the core, that
  is important information about the abstraction — report it in an issue rather than
  working around it.**

## Building and testing

Requires Go 1.26+, Node 20+ with pnpm, and Docker.

```bash
go build ./... && go vet ./... && go test ./...
go test -race ./...
gofmt -l ./internal ./cmd ./lab ./tools     # must print nothing

make lab-up          # real FreeRADIUS, Postgres, the Go control plane — no hardware
make lab-nas-auth    # a real Access-Request as a NAS, both outcomes
make lab-nas-acct    # the accounting start/stop, and the radacct row
make lab-test        # tier 2, and it fails rather than skipping
make check-all       # the whole checklist, Go and frontend

cd web && pnpm install && pnpm dev                # mock + portal + admin together
cd web && pnpm -r typecheck && pnpm -r test
```

`lab/eap` and `lab/policy` have no build tags, so `go test ./...` runs them
already. Without Docker they skip. `make lab-test` supplies the environment
they need — a usable daemon, a FreeRADIUS image checked to be 3.2.x rather
than merely present, `FOYER_LAB_E2E=1` and a generated `lab/raddb` — and then
asserts each tier-2 test by name, because `go test` prints `ok` for a package
whose tests all skipped.

New EAP behaviour needs an `eapol_test` case. `radclient` does not speak EAP, so
without one the change is untested no matter how green CI looks.

When you add a test for a bug, make it fail against the unfixed code first. A test
that has never been red is not yet evidence. Never skip a test to make CI pass — fix
it, or delete it with the reason in the commit message.

## Commit messages and pull requests

`scope: what changed`, imperative mood. Explain *why* in the body — the diff
already shows what. Keep a pull request to one concern, and fill in the template:
what changed, why, how you verified it, and which invariants it touches.

## Conduct

Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).

## Security issues

Do not open a public issue. See [SECURITY.md](SECURITY.md).
