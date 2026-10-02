# Deployment rules — the server is an artefact, not a workspace

> Governs `deploy/`, the `Makefile` and `bin/`. The operator-facing guide is
> [`deploy/README.md`](../../deploy/README.md).

A Foyer deployment host is a clean Ubuntu 24.04 machine that `deploy/install.sh`
turns into a running installation. Whoever runs it — a venue, an integrator, a
contributor testing a release — the rules below are the same.

---

## The rule

**Every change to a deployment host ships as code.** Nothing is configured by hand:
not a package, not a config file, not a firewall rule, not a cron entry, not a
one-off `docker run`.

If a change is not expressed in `deploy/`, it did not happen — and the next rebuild
will prove it, painfully, at the worst moment.

## Why a promise is not enough, and what replaces it

A rule against hand-editing cannot be enforced by intention. Someone will SSH in at
2 a.m. to fix an outage, and they will be right to. What makes the rule real is that
**the machine is rebuildable, and the rebuild is exercised** — destroying it and
recreating it from `deploy/` has to yield the same working system. That is the only
honest test of the claim: a guarantee you have never made fail is not evidence.

So:

- **An ad-hoc command on the box is a defect, not a shortcut.** The defect is that
  the command is absent from `deploy/`. Back-port it immediately — an hour later
  nobody remembers the flag.
- **Fix forward through the repository, including during an incident.** If the outage
  is severe enough to justify hand-editing, it is severe enough to justify a commit
  immediately afterwards, before the terminal is closed.
- **A manual command is a diagnostic, never the fix.** `ssh`, `docker exec`, `psql`
  and reading logs are how you find out what is wrong; what makes it right is a change
  to this repository. A user never ssh's, never types a command, never opens a Docker
  client: they install from `deploy/`, sign in at the console, and are finished.
- **Record what you did on the box even when you back-port it**, in the pull request
  that carries the back-port. A repository that matches the machine by luck rather
  than by discipline looks identical to one that matches by construction, until it
  does not.

## The tension with secure defaults, stated because it is where this goes wrong

[`security.md`](security.md) §7 requires **first-boot generation** of the database
password, signing keys and RADIUS secrets, **no administrator password at all** (the
installer prints a one-time setup code instead, `deploy/consolesetup.sh`), and **no
static credentials anywhere in the repository.**

"Ship as code" therefore does **not** mean "commit the configuration". It means
commit the **mechanism**: the generator, the systemd unit, the compose file, the
migration. The generated values live on the machine and in nothing that is version
controlled. A `.env` with a real secret in a commit is a violation of §7, not
compliance with this file — and it is the most likely way these two rules get
resolved wrongly.

Secrets in transit to the box go through a channel that is not the repository. Where
one is needed, `deploy/` declares **that** it is needed and what shape it takes, never
its value.

**Each container holds only the logins its job needs.** The control plane holds
`foyer_app` and the KEK; the stale-session reaper's and the retention role's logins
are the `maintenance` service's (`foyer maintenance`, the same image, no KEK —
PacketFence's `pfcron` shape); FreeRADIUS's database password is the `freeradius`
service's, and the tree the control plane generates names it as
`$ENV{FOYER_RADIUS_DB_PASSWORD}`. A secret added to a service is a question of which
job needs it, not which container is convenient — `tools/deploycheck`
`TestTheControlPlaneHoldsNoLoginItDoesNotNeed` reads `compose.yaml` for it, and
`deploy/verify.sh` reads the running containers.

**Operator settings are not deployment configuration.** Every setting a feature needs
has a console screen backed by a database row and takes effect without a restart; the
`FOYER_*` variables `deploy/` writes are install-time values only
([`docs/operator/configuration.md`](../operator/configuration.md)). A new feature that
needs an environment variable, a config file or a CLI step from the operator is
incomplete.

## What lives in `deploy/`

| | |
|---|---|
| `install.sh` | A clean Ubuntu 24.04 host to a running Foyer in one command — bootstrap, tree, first boot. Idempotent, re-runnable |
| `compose.yaml` | The running system. Images pinned by digest, never by a floating tag |
| `bootstrap.sh` | The substrate on its own: Docker, the deploy user, the layout, the package baseline. Idempotent |
| `first-boot.sh` | Generates every secret once, migrates, starts, opens the console setup |
| `up.sh` | Start or restart from the shipped tree, with `FOYER_REV` set |
| `sync.sh` | Optional: ship the committed tree from a workstation (`FOYER_HOST` required) |
| `verify.sh` | Asserts the machine matches the repository — revision, tree manifest, image digests, no package outside the baseline, secrets, logins, certificates, backups |
| `backup.sh` | Dump, prune by age and by `FOYER_BACKUP_KEEP`, and report. `--install` writes the systemd timer that runs it |
| `backups.sh` | Sourced, never executed: naming, the KEK fingerprint, the listing, the manifest fields |
| `restore.sh` | Checksum, then the KEK, then `--dry-run`, then the question, then drop/load/logins/up/verify. **A restore without the KEK refuses by name** |
| `upgrade.sh` | The in-place answer to a change, and the one that calls `backup.sh` before it touches anything |
| `support-bundle.sh` | Host-side diagnostics, redacted through `foyerctl support-bundle` |
| `publish.sh` | The maintainer's: build, push, and write the registry's digests into `images.conf` |

### The rebuild is not a promise, it is a command

The claim that a host can be destroyed and recreated from this directory is only
worth something if it is exercised — and exercising it finds things nothing else
does.

A directory a bind mount needs must be created by `first-boot.sh` with the right
owner: if Docker creates a missing bind-mount source it is `root:root`, and a non-root
process can chown only to the uid it already has, so nothing later can repair it.

**A red check that is not a defect is worse than a missing one**, because the operator
learns to ignore the tool and then skips the run that mattered. A test such as `-e`
inside a directory the deploy user cannot read answers false — which reads as
"absent", and for a check that a key is *not* readable, fails open. `verify.sh`
therefore answers three states for anything under `${SECRETS}`: present, absent, and
**unreadable**.

**The KEK is the one value a backup cannot carry, and the manifest says so.**
`FOYER_KEK` is deliberately not in the database it protects, so a `pg_dump` restored
without it restores ciphertext nobody can read. `verify.sh` therefore asks whether
*this* host's KEK is the one the newest dump was taken under — so "your backups are
ciphertext" is discovered before the night it matters rather than during it.

### What `verify.sh` can and cannot answer

It answers whether the machine matches this repository, and it answers *whether a
restore is possible*: the backup directory and its mode, the timer active and enabled,
a dump newer than `FOYER_BACKUP_MAX_AGE_HOURS`, its manifest, its `sha256`, and the KEK
fingerprint agreeing. It does **not** answer whether a restore succeeds, and it does
**not** answer whether `/metrics` is healthy — that listener is deliberately
unpublished, so there is no socket on the host to ask. Both are reported as what they
are, which is the whole of the three-state rule above applied to checks rather than to
secrets.

`lab/` is **not** `deploy/`. The lab exists to be broken, runs with published ports
and a known password, and its compose file must never be pointed at a real
deployment host.

### Base images

Every `FROM` names what it is in its tag — the exact Go release on a golang
builder (`golang:1.26.8-alpine3.24`, not `1.26-alpine`), the alpine branch on
anything alpine-based — and the deployment's are pinned by digest as well. The
tag is what a person reads; the digest is what Docker uses. A floating
`1.26-alpine@…` can be any patch release and says nothing of which, and an
unnamed alpine branch can run months past its end of support before anyone reads
the date.

`tools/supplycheck` enforces it inside `go test ./...`: the Go release at or
above go.mod's `go` line and the patched floor, and the alpine branch outside a
90-day margin of alpine's own end-of-support date. **It reads the wall clock, so
it can go red on a tree nobody touched** — and when it does the fix is to move the
base, not the margin. `make vulncheck` (in `make ci`) is the half that needs the
network: it notices when the floor itself has to move.

The same gate holds PostgreSQL's major, ubuntu's release and debian's release
(or a dated sid snapshot's age) to each project's own end of support, and reads
`deploy/images.conf` as well as the Dockerfiles — so the runtime `postgres` tag
names its alpine branch too. **A tag and its digest can disagree, and offline
nothing can tell.** `make supply-verify` (in `make ci`) asks the registry that
every pinned tag names its pinned digest today and runs `govulncheck -mode=binary`
on what `deploy/Dockerfile.foyer` builds; a tag that moved is a failure whose fix
is `make supply-pin`, a reviewed diff and a commit.

## The host is also the release gate's stopwatch

[`docs/architecture.md`](../architecture.md) §12 makes *"a stranger installs it and a
phone gets online"* a timed release criterion, and notes that `docker compose up` was
never the hard part — the minutes that matter are after the container starts:
configuring the AP, obtaining a certificate the guest's phone trusts, creating a
controller API key, making DNS resolve pre-authentication.

So a release is timed on a real host with a real access point and a real phone.
**Time it on a freshly built host, not on a host that has been nursed** — a nursed
host measures the operator's memory rather than the product.
