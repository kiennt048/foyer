# deploy/ — installing and running Foyer

This directory is a Foyer deployment, as code: the installer, the compose file, the
scripts that start, upgrade, back up, restore and verify an installation. It targets
**a clean Ubuntu 24.04 host** that you control — physical, virtual or cloud — on the
same network as your access points.

**The rule for everything here:** every change to the host ships as code. Nothing is
configured by hand on the machine. [`docs/rules/deployment.md`](../docs/rules/deployment.md)
explains why and what that means for secrets.

Placeholders below: `<deploy-host>` is the machine's address, `<deploy-user>` the
account you install as (any account with `sudo`).

---

## Requirements

| | |
|---|---|
| OS | Ubuntu 24.04 LTS. The installer refuses anything else rather than guess about the base of a security product |
| Memory | 2 GB is enough (see *The memory budget*); the installer warns below 1.5 GB and when there is no swap |
| Network | the host must be reachable from the access points on UDP 1812/1813, and from guests on TCP 80 (and 8880 for UniFi) |
| Software | nothing — the installer adds Docker from Docker's own repository |

## Install

From a checkout of this repository on the host:

```bash
sudo bash deploy/install.sh
```

One script, start to finish, and every step is idempotent:

1. refuses anything that is not Ubuntu 24.04, and refuses to run unprivileged;
2. records the package baseline `verify.sh` later compares against — **before**
   installing anything;
3. installs Docker;
4. creates `/opt/foyer`, its `secrets/` directory (`0700`) and the deploy user's
   access, copies the tree into `/opt/foyer/src` and writes an integrity manifest of
   it;
5. generates every secret on this host, once, and never rotates one that exists;
6. pulls the published images, pinned by digest (`deploy/images.conf`);
7. migrates the database and starts the stack — the control plane generates the
   FreeRADIUS configuration itself;
8. opens the console's first-access setup and prints its **one-time setup code**.

It does **not** configure a venue, an access point or a certificate. Those are the
operator's, in the console, and the installer prints what is left.

**Re-run it** after an upgrade or to repair permissions: it regenerates nothing and
enforces everything.

**Building instead of pulling.** When `images.conf` names no published digest, the
installer refuses rather than build silently. `FOYER_ALLOW_UNPINNED=1` makes it
build the images on the host — a development convenience that produces images
nobody else can verify, and that `verify.sh` reports as unpinned.

The three steps `install.sh` combines also exist separately, for a host that is
provisioned in stages: `sudo bash deploy/bootstrap.sh` (Docker, the deploy user, the
layout), then a tree in `/opt/foyer/src` (from a checkout, or `sync.sh` below), then
`sudo bash deploy/first-boot.sh` (secrets, start, setup code).

## First run

Foyer ships **no administrator password** and no default credential of any kind. A
fresh install has no operator and no site, on purpose.

| | | |
|---|---|---|
| 1 | Open `https://<deploy-host>:8443` from a browser on the same network | **the browser warns once, and that is expected** — the console uses a self-signed certificate. Compare the SHA-256 fingerprint the installer printed before accepting. A *name* mismatch is a different warning and is not expected: it means the certificate is for a machine you did not dial |
| 2 | Enter the **console setup code** the installer printed, with a username and password of your own | it creates the first administrator, once; two browsers racing for it get one administrator and one refusal |
| 3 | **Create the site** | a site is a venue, so its name is yours; none is seeded |
| 4 | Register the controller or access point | a device is refused without a site |
| 5 | Check **Network → RADIUS clients** | every device change is written to the table FreeRADIUS reads at once; the card says when it last synced and what changed, and **Sync now** repeats it |
| 6 | Point the access point at this host | the access point's page in the console shows its probe and how each capability is known; `foyerctl doctor` is the support tool when that is not enough |

**Lost the setup code?** Run `sudo bash deploy/install.sh` (or `first-boot.sh`)
again. While nobody has completed the setup, each run prints a new code and retires
the previous one; once an administrator exists, it prints and opens nothing. The code
is 80 random bits, shown once and stored only as a SHA-256 — it never becomes a
password. Why a code rather than a first-come page: the console is published on every
interface, so a page anybody could reach first would hand the RADIUS server to
whoever on the venue LAN opened it before you.

`foyerctl create-operator --must-change-password` exists for handing somebody a
temporary password from the host: the API refuses everything except reading the
session, signing out and changing the password until they choose their own.

## Settings are in the console, not here

Everything in this directory is the **installation**: what the scripts generate,
wire and verify. Once it is up, every operator setting — the portal's address and
certificate, the maintenance schedule, retention, FreeRADIUS, sign-in limits, SMS
provider credentials — is changed in the admin console, stored in the database and
in force without a restart. The `FOYER_*` variables these scripts write are
install-time values no operator sets; [`docs/operator/configuration.md`](../docs/operator/configuration.md)
lists every one of them, and every console screen, and says which is which.

## Day-to-day operations

All of these run on the host as the deploy user unless shown with `sudo`.

```bash
deploy/up.sh                         # start or restart the stack from /opt/foyer/src
deploy/verify.sh                     # does this machine still match the repository?
deploy/upgrade.sh                    # upgrade to the tree in /opt/foyer/src
deploy/backup.sh                     # one backup now, prune, report
sudo bash deploy/backup.sh --install # install the daily backup timer
deploy/restore.sh --dry-run          # check the newest backup, change nothing
deploy/support-bundle.sh             # a redacted diagnostics archive
```

**Drive compose through these scripts rather than by hand.** `FOYER_REV` has to be
set when the images are built — it becomes the image tag, the
`org.opencontainers.image.revision` label, **and the revision linked into the
binaries** (`foyerctl version`, the control plane's first log line, the support
bundle's manifest). A bare `docker compose up` builds `foyer:unknown`, and
`verify.sh` reports that as drift: a host that cannot name its own revision has
drifted whether or not the bytes match. `deploy/compose.sh` is the wrapper the
scripts use when you need compose directly (`deploy/compose.sh logs foyer`).

### Shipping a tree from a workstation (optional)

`deploy/sync.sh` pushes the **committed** tree from a workstation checkout to a host
over SSH, instead of checking the repository out on the host:

```bash
FOYER_HOST=<deploy-host> deploy/sync.sh
```

`FOYER_HOST` is required; there is no default host. The SSH user and private key
come from `FOYER_USER` and `FOYER_SSH_KEY` (the script's header documents their
defaults). It ships `git archive HEAD` — so **uncommitted work cannot reach
the host** — refuses a dirty working tree (`--allow-dirty` overrides it), writes the
revision to `/opt/foyer/src/.shipped-rev`, and records a checksum manifest of what it
shipped in `/opt/foyer/state/src.sha256`. Then run `deploy/up.sh` (or `upgrade.sh`)
on the host. `verify.sh --remote` runs from the same workstation with the same
variables and adds the checks that need the git object database.

## What is published, and what is not

| Port | | |
|---|---|---|
| `80/tcp` → portal | **HTTP** | every unauthenticated device on the SSID must reach it |
| `8880/tcp` → portal | **HTTP, UniFi's own port** | the controller's External Portal field takes a bare IP and the AP redirects to `<ip>:8880/guest/s/<site>/` |
| `1812/udp`, `1813/udp` | RADIUS | the access points send authentication and accounting here |
| `8443/tcp` → console | **HTTPS** | the admin console, on a self-signed certificate the control plane generates |
| `443/tcp`, `8843/tcp` → portal | **HTTPS, optional, off by default** | nothing answers until the operator uploads a certificate in the console — see [`docs/portal-https.md`](../docs/portal-https.md). 8843 is UniFi's HTTPS portal port |
| *not* `5432` | | PostgreSQL is on an internal network with no gateway |
| *not* `8080` | | the Policy API answers FreeRADIUS and nothing else |
| *not* `18120`, `11812`, `11813` | | FreeRADIUS's status probe and the blue/green candidate server |
| *not* `/metrics` | | the metrics listener is deliberately unpublished |

Test RADIUS from **another machine** (or the host's LAN address), never from
`127.0.0.1` on the host: Docker rewrites the source of loopback traffic to a
published UDP port to its bridge gateway, so FreeRADIUS sees an unknown client. Traffic
from real access points keeps its source address.

**The portal and the console are two different questions with two different
answers.**

### The portal: HTTP by default, HTTPS when the operator supplies a certificate

A guest reaches the portal through captive-portal interception, inside a Captive
Network Assistant on a device that has no internet yet and no way to check anything.
So **HTTP is the default, TLS is the operator's choice, and the guest is never shown
a certificate warning nobody chose.** The portal generates no certificate of its own.

To serve HTTPS, upload a certificate and key together with the `https://` portal
address in the console under **Network → Portal certificate**. The portal serves
HTTPS on 443 and 8843 from the next handshake — no re-run, no restart — while 80 and
8880 keep receiving the NAS redirect and send every guest on. A pair placed in
`/opt/foyer/secrets/portal-tls` (`portal.crt`, leaf first then the chain, and
`portal.key`) is the installation's bootstrap fallback and the place an ACME renewal
hook can write; a certificate saved in the console wins over it. Which certificate,
what the walled garden must allow, and what RFC 8908 needs:
[`docs/portal-https.md`](../docs/portal-https.md).

### The console: HTTPS, self-signed, generated by the application

- **The control plane generates the certificate**; `first-boot.sh` creates
  `/opt/foyer/secrets/admin-tls` empty and owned by the container's user, and the
  control plane fills it on first start. No operator types a parameter.
- **It is valid for this host's addresses.** `deploy/consolenames.sh` computes them
  from `hostname -I` and `hostname -f`, and `compose.yaml` passes them as
  `FOYER_ADMIN_TLS_NAMES`. A stored certificate that does not cover a declared name is
  replaced on the next start; one that does is left alone.
- **Validity is 825 days**, because Apple's certificate evaluator refuses a
  self-signed leaf trusted as its own anchor beyond that (826 days fails with
  `CSSMERR_TP_CERT_SUSPENDED`). Installing the certificate does not lift that limit.
- **It renews itself, and its fingerprint changes when it does.** Renewal is checked
  on every TLS handshake, so a host that is never restarted still renews before
  expiry; the control plane logs the new fingerprint. A renewal that cannot be written
  keeps serving the old certificate rather than failing the handshake. Re-read the
  fingerprint on the host with
  `openssl x509 -in /opt/foyer/secrets/admin-tls/admin.crt -noout -fingerprint -sha256`.
- **It is a single self-signed leaf, not a per-install CA** (`CA:FALSE`). Trusting one
  leaf trusts one certificate for one host; trusting a root would trust anything it
  could mint.
- **Accept the warning once per browser and compare the fingerprint** rather than
  installing the certificate. The fingerprint is the only thing that tells the
  expected warning from an attacker's, and `verify.sh` asserts that the certificate the
  console presents is the one the fingerprint was taken from.
- **From off-site, a tunnel is the safer habit:**
  `ssh -L 8443:127.0.0.1:8443 <deploy-user>@<deploy-host>`, then
  `https://127.0.0.1:8443`.

## Secrets

Foyer requires first-boot generation of every secret and **no static credential
anywhere in the repository** ([`docs/rules/security.md`](../docs/rules/security.md) §7).
So:

- **`deploy/` commits the mechanism, never the configuration.** `first-boot.sh` is
  here; what it generates is not, and never will be. A `.env` with a real value in a
  commit is a violation, not compliance.
- Generated values live in `/opt/foyer/secrets`, mode `0700`, created before anything
  can write into it with the wrong mode.
- `openssl rand` reads the kernel CSPRNG — the same source `crypto/rand` uses. Never
  `$RANDOM`, never a timestamp.
- Database passwords are **hex**, because they are interpolated into a `postgres://`
  URL and a base64 password containing `/` or `+` parses into a different password.
- Secrets reach containers as **files** (docker secrets, `*_FILE` variables), never as
  environment values visible to `docker inspect`. Setting a variable and its `_FILE`
  form together is refused rather than resolved by precedence. `verify.sh` asserts the
  running containers carry no secret in their environment.

### `FOYER_KEK` — keep a copy somewhere else

The key-encryption key protects every RADIUS shared secret, every controller
credential and every NT hash. It is **deliberately not in the database it protects**,
so **losing it makes all of them permanently unreadable**, and a database backup
restored without it restores ciphertext.

**Keep a copy of `/opt/foyer/secrets/kek` somewhere a failure of this host's disk
cannot reach.** The installer and every backup say so; the backup tools record the
KEK's fingerprint (never the key) so a mismatch is discovered before the night it
matters.

### Database logins, and which container holds which

The schema owner is a superuser and is used only by the one-shot `migrate` service
and by `pg_dump`. The running services log in as least-privilege roles:

| Login | Used by | Can |
|---|---|---|
| `foyer` (superuser) | the `migrate` one-shot, backups | everything; owns the schema |
| `foyer_app` | the control plane (`foyer` container) | its grants: SELECT only on `radacct`/`radpostauth` |
| `foyer_radius` | FreeRADIUS (`rlm_sql`) | read `nas` and the credential view, write accounting and post-auth, nothing else |
| `foyer_reaper`, `foyer_retention` | the `maintenance` container | close a stale session; delete what a retention period or an erasure removes |

| Container | Holds | Why |
|---|---|---|
| `foyer` | `database_url` (`foyer_app`), `kek`, `maintenance_token` | the console, the portal, the Policy API |
| `maintenance` | `database_url`, `reaper_database_url`, `retention_database_url`, `maintenance_token`; **no KEK** | scheduled cleanup and the retention half of every erasure |
| `freeradius` | `radius_db_password` | `rlm_sql`; the generated tree reads it as `$ENV{FOYER_RADIUS_DB_PASSWORD}` |
| `migrate` (one-shot) | the owner's URL and every login's password, which it sets | schema and logins |

The control plane — the process that parses every console request and every guest's
form — is the container an attacker is likeliest to reach, so it holds only its own
login and the KEK. `cmd/foyer` refuses to start on a login that holds more than
`foyer_app`, and `verify.sh` asks the running database who is connected and whether
`RESET ROLE` gains anything.

**Rotating a database password:** replace the file in `/opt/foyer/secrets`, run
`sudo bash deploy/first-boot.sh --secrets-only`, then `deploy/up.sh`. A changed
control-plane login is **staged** as `database_url.next` rather than overwritten,
because a running control plane re-reads it on any restart and the database does not
accept the new password until `migrate` has set it; `up.sh` promotes it just before
starting the stack, after `migrate` has run (`deploy/logins.sh` has the reasoning —
the same pending → set → current order AWS Secrets Manager uses).

### The Policy API boundary is mutual TLS

FreeRADIUS and the control plane are separate containers, so the `rlm_rest` boundary
is **mTLS**: `issue-radius-cert.sh` issues FreeRADIUS a client certificate from the
CA `first-boot.sh` generates, the generated `rest` module presents it, the generator
refuses to write an `https` policy URL without it, and `cmd/foyer` refuses to start
with TLS and no client CA on a non-loopback address. A FreeRADIUS presenting no
certificate, or another CA's, is refused — and the guest is rejected either way: the
boundary fails closed.

## Upgrading

The first person to install must be able to upgrade. Put the new tree in
`/opt/foyer/src` (a `git pull` in a checkout followed by `sudo bash deploy/install.sh`,
or `sync.sh` from a workstation), then:

```bash
deploy/upgrade.sh
```

The order is the safety argument:

1. **Back up**, with `deploy/backup.sh` — the same script the daily timer runs, so the
   upgrade's backup is one `restore.sh` can read.
2. **Secrets**: whatever the new tree needs and this install lacks. A changed
   database login is **staged**, not switched.
3. **Images**: pull the pinned ones (or build them, when `images.conf` names none)
   while the old ones are still serving.
4. **Stop** `foyer`, `freeradius` and `maintenance`; PostgreSQL stays up.
5. **Migrate** as a one-shot. It must exit 0. It grants any new login.
6. **Switch** to the staged login (`first-boot.sh --promote-logins`).
7. **Start** the new images and wait for health.
8. **Verify** with `deploy/verify.sh`.

The database is migrated **with the application stopped**: in the other order the old
binary serves live guests against a schema it was not built for, which is a different
failure on every request and reads as flakiness. The portal and RADIUS are down
between steps 4 and 7 — a container restart plus however long the migrations take.

**When a migration fails part way:** goose wraps each migration in a transaction, so
the database is left at the last version that *fully* applied — never half of one.
`upgrade.sh` stops and starts nothing. Then:

- `foyerctl migrate --status` names what applied and what did not. Read-only.
- `foyerctl migrate --to <version>` runs the down migrations; then put the previous
  revision back in `/opt/foyer/src` and run `upgrade.sh` again.
- The backup from step 1 is the answer when the down migrations are not enough — a
  `DROP COLUMN` reverses cleanly in schema and not in data.

## Backups and restore

```bash
sudo bash deploy/backup.sh --install    # systemd timer: daily at 03:17, Persistent=true
deploy/backup.sh                        # one now, prune to FOYER_BACKUP_KEEP (default 14), report
deploy/backup.sh --list                 # what is in the backup directory, no dump taken
deploy/restore.sh --list                # the same, from the restore side
deploy/restore.sh --dry-run             # check the newest dump, change nothing
deploy/restore.sh                       # restore the newest dump, with a question first
deploy/restore.sh <dump>                # a named dump
deploy/restore.sh --kek <file> <dump>   # with the KEK from somewhere else (e.g. a new host)
deploy/restore.sh --yes                 # no question asked
```

Backups are written to `/opt/foyer/state/backups` (`0700`), each with a manifest
recording its checksum and the KEK's fingerprint. A dump is written as `.partial` and
renamed when complete, so an interrupted dump is never mistaken for a whole one; an
empty dump, or a non-numeric or zero `FOYER_BACKUP_KEEP`, is refused.

**A restore checks, in order:** the dump against its manifest's checksum, then the
KEK, then — with `--dry-run` — stops; otherwise it asks, then stops the application,
drops and recreates the database (so a restore lands whole or leaves an empty
database, never half of each), loads the dump, re-grants the least-privilege logins,
starts the stack and runs `verify.sh`. **A restore without the matching KEK refuses
by name** rather than producing an installation nobody can decrypt.

What it restores is the **database** — venues, vouchers, accounts, sessions, audit
entries and every sealed column. It does not restore `/opt/foyer/secrets`: database
passwords, TLS material and the maintenance token are regenerated by `first-boot.sh`,
and your administrator survives because the database does — you sign in to a restored
console with the password you already had.

## Verifying the machine

`deploy/verify.sh` asserts that the host matches the repository and **names the
object** in every failure:

- the revision the host runs, on the image label and inside the binaries, and that
  the two agree (a mismatch is a re-tagged image);
- the shipped tree against its checksum manifest — contents **and** the file set, so
  an added file is drift too;
- image digests, and no package outside the recorded baseline;
- the secrets layout and modes, answered in three states — present, absent, and
  **unreadable** — so a check that cannot see a file never reports it as missing, and
  never passes because it could not look;
- which login each container holds, and that no secret is in a container's
  environment;
- the console certificate on the wire against the file its fingerprint came from;
- the portal's HTTP/HTTPS state;
- backups: the directory and its mode, the timer active and enabled, a dump newer
  than `FOYER_BACKUP_MAX_AGE_HOURS` (48), its manifest and checksum, and the KEK
  fingerprint matching this host's KEK.

It answers whether a restore is **possible**, not whether one succeeds, and it does
not answer whether `/metrics` is healthy — that listener is unpublished. Both are
reported as what they are. Exit status is 0 when the machine matches, non-zero with
a count when it does not.

Run it after every change, and again **at least thirteen hours after a deploy**:
some failures — a container slowly exhausting its PID limit, for example — are
invisible in the first hours of a fresh start. (`init: true` on the services exists
for exactly that one: a healthcheck that forks a TLS helper leaks one zombie per probe
into a PID 1 that reaps nothing.)

## Sending logs

```bash
deploy/support-bundle.sh [--out /path/foyer-support.tar.gz]
```

It gathers what a container cannot see — Docker's logs, the resolved compose
configuration, the listeners, the generated FreeRADIUS tree — and pipes all of it
through `foyerctl support-bundle`, which redacts. Redaction is the feature:
**`radiusd -X` output contains every shared secret in cleartext**, and the generated
SQL module carries a database password. The redactor matches the *shape* of a
credential rather than a list of known values, and `manifest.json` reports how many
substitutions each rule made. Read the bundle before you send it anywhere.

## The memory budget

PostgreSQL, FreeRADIUS, the control plane and the maintenance service fit in
**under 2 GB**. Every limit in
`compose.yaml` has its arithmetic beside it:

| | limit | why |
|---|---|---|
| PostgreSQL | 512 MB | `shared_buffers` 128 MB is 25% *of the limit*. `work_mem` 2 MB × `max_connections` 80 = 160 MB worst case |
| control plane | 320 MB | with `GOMEMLIMIT=256MiB` — a Go process under a cgroup limit without one gets OOM-killed rather than collecting harder |
| FreeRADIUS | 384 MB | the resident set is small; what has to fit is the thread pool |
| maintenance | 128 MB | the scheduled jobs and the erasure door; an application pool of 4 connections |
| one-shot | 192 MB | `migrate`, not concurrent with the steady state |

The steady-state limits sum to 1 344 MB (about 1.3 GB), leaving roughly 700 MB for
the kernel, dockerd, sshd and the page cache on a 2 GB host. `max_connections` 80 is
accounted for: FreeRADIUS's `rlm_sql` pool, the control plane's pool, the maintenance
service's pools, the one-shots, `foyerctl` and three superuser slots. Raising FreeRADIUS's worker count in the console is bounded by the
same budget ([`docs/rules/radius.md`](../docs/rules/radius.md)).

## Rebuilding from nothing

The honest test of "everything ships as code" is to destroy the machine and build it
again: a clean Ubuntu 24.04 host, `sudo bash deploy/install.sh`, `deploy/restore.sh
--kek <file>` if there is data to bring back, and `deploy/verify.sh`. If the same
working system does not come back, the difference is a defect in this directory.

**Time a first install on a fresh host, not one that has been nursed.** The minutes
that matter come after the containers start — configuring the access point, a
certificate the guests' phones trust, a controller API key, DNS that resolves before
authentication — and a nursed host measures the operator's memory rather than the
product.

## Building and publishing images

`deploy/publish.sh` is the maintainer's script, not an operator's: it builds the two
Foyer images, pushes them, and writes the registry's **digests** back into
`deploy/images.conf`, so the file records what the registry holds rather than what
someone intended to push. An operator runs `install.sh` and never builds anything.

`lab/` is **not** `deploy/`. The lab exists to be broken, runs with published ports
and test credentials, and its compose file must never be pointed at a real
deployment. See [`lab/README.md`](../lab/README.md).
