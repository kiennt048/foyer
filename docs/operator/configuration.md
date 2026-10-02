# Where every setting lives

*For the operator of a Foyer deployment, and for whoever writes the next document
that tells them to change something.*

> **Tóm tắt (Tiếng Việt).** Mọi thiết lập của người vận hành nằm trong **bảng quản
> trị** (admin console), được lưu trong cơ sở dữ liệu và có hiệu lực ngay, không cần
> khởi động lại. Người vận hành không bao giờ phải sửa biến môi trường, tệp cấu hình
> hay chạy lệnh `foyerctl`. Các biến `FOYER_*` còn lại chỉ dùng **lúc cài đặt**:
> `deploy/first-boot.sh` tự sinh và ghi chúng — không ai phải gõ.

**The rule:** every setting an operator changes is
changed in the admin console — stored in the database, in force without a restart,
written to the audit log. Nobody operating Foyer edits an environment variable, a
configuration file or runs a command-line tool to change how it behaves. Secrets are
write-only: the console accepts them and never shows them again.

The field does the same thing and draws the same line. Grafana, Keycloak and
authentik keep the settings a person changes in the database behind the UI, and
keep a short list of *bootstrap* values — the database URL, the first admin, the
listen address — in the environment, read once at start (`KC_DB_URL` and
`KC_BOOTSTRAP_ADMIN_*`, `AUTHENTIK_POSTGRESQL__*` and `AUTHENTIK_BOOTSTRAP_*`).
UniFi and Omada keep every operator setting in the controller's UI, with a
properties file only for ports and the like. Foyer follows the same line: the
environment is for the **installer**, and `deploy/first-boot.sh` is the installer.

## The console

| To change | Open | Needs |
|---|---|---|
| The venue: name, languages, the network name printed on slips, terms, look, photograph, how guests sign in | **Portal** | administrator (`portal:manage`); staff can read |
| Phone sign-in: provider, brandname, message, limits, the daily message cap, a test message | **Portal → Phone sign-in** | administrator |
| The site's time zone | **Portal → Time zone** | administrator |
| The guest Wi-Fi password printed on slips and in their QR code | **Portal → Network** | administrator |
| The portal's public address, the venue information page, the sign-in throttles, request size ceilings | **Network** | administrator (`nas:manage`); staff can read |
| The portal's HTTPS certificate | **Network → Portal certificate** — [`../portal-https.md`](../portal-https.md) | administrator |
| FreeRADIUS worker threads, EAP-TLS certificate, applying them | **Network → FreeRADIUS** | administrator |
| The RADIUS client table FreeRADIUS reads | **Network → RADIUS clients** — synced automatically; **Sync now** repeats it | administrator |
| Disconnect (CoA) timing per router | **Network → Disconnect timing** | administrator |
| Controllers and access points, shared secrets | **Controllers**, **Access Points** | administrator |
| What guests are asked for, marketing consent | **Guest Data** | administrator |
| How long each kind of personal data is kept; erasing one person | **Privacy** — [`personal-data.md`](personal-data.md) | administrator (`privacy:manage`) |
| The maintenance schedule: interval, stale-session threshold, per-pass ceiling, retention batch | **Privacy → Maintenance** | administrator |
| Vouchers and guest accounts, including a guest account's 802.1X password | **Vouchers**, **Accounts** | staff or administrator |
| Console users and roles | **Operators** | administrator |
| Your own password | the account menu → change password | anyone signed in |

Every change above is in force at the next request, or — for FreeRADIUS, which fixes
its settings when it starts — when you press **Apply and restart FreeRADIUS**, which
says whether this installation can restart it.

## What is still in the environment, and why it is not a setting

Each of these is written by `deploy/first-boot.sh` (or `deploy/compose.yaml`) at
install time and read once at start. **An operator never sets one.** They fall into
three kinds.

**Secrets and logins the installer generates** — never typed by anyone, passed to
the containers as docker secrets rather than variables, and asserted absent
from the environment by `deploy/verify.sh`:
`FOYER_KEK(_FILE)`, `FOYER_DATABASE_URL(_FILE)`, `FOYER_REAPER_DATABASE_URL(_FILE)`,
`FOYER_RETENTION_DATABASE_URL(_FILE)`, `FOYER_POSTGRES_PASSWORD(_FILE)`,
`FOYER_APP_DB_PASSWORD`, `FOYER_RADIUS_DB_PASSWORD`, `FOYER_REAPER_DB_PASSWORD`,
`FOYER_RETENTION_DB_PASSWORD` (the last four read only by the `migrate` one-shot),
`FOYER_MAINTENANCE_TOKEN(_FILE)`. The reaper's and the retention role's URLs are the
**`maintenance`** container's, not the control plane's,
and FreeRADIUS's database password is FreeRADIUS's own: the control plane refuses to
start holding a maintenance login beside `FOYER_MAINTENANCE_URL`, and `verify.sh`
says so if it does.

**The maintenance container** (`foyer maintenance`, the same image): the
stale-session reaper, the retention sweep and the other cleanup jobs, and the
retention half of every erasure, in a container that alone holds the two maintenance
logins and no KEK. `FOYER_MAINTENANCE_URL` tells the control plane where it is,
`FOYER_MAINTENANCE_ADDR` is where it listens, and `FOYER_RADDB_POSTGRES_PASSWORD_ENV`
names the variable the generated FreeRADIUS tree reads its database password from
(`$ENV{FOYER_RADIUS_DB_PASSWORD}`), so the control plane never holds that password.
The maintenance *schedule* is a console setting: **Privacy → Maintenance**.

**Where the process is wired in** — listen addresses, certificate paths inside the
container, the generated FreeRADIUS tree:
`FOYER_ADMIN_ADDR`, `FOYER_PORTAL_ADDR`, `FOYER_PORTAL_TLS_ADDR`, `FOYER_POLICY_ADDR`,
`FOYER_POLICY_HOST`, `FOYER_ADMIN_TLS_CERT`/`_KEY`, `FOYER_ADMIN_TLS_NAMES` (computed
from the host by `deploy/consolenames.sh`), `FOYER_POLICY_TLS_CERT`/`_KEY`,
`FOYER_POLICY_CLIENT_CA`, `FOYER_RADDB_TARGET` and `FOYER_RADDB_POLICY_*`.
`FOYER_RADDB_TARGET` is the handoff directory the control plane shares with the
FreeRADIUS container: the console's Apply writes the tree there and the supervisor in
the FreeRADIUS image validates it, restarts FreeRADIUS on it and rolls back on failure
(`internal/raddb/handoff.go`). There is no restart command anywhere and no variable
that holds one, because a stored command the control plane executes would hand anyone
with `nas:manage` a shell on the host. The console says so when an installation has no
handoff directory (`applyAvailable` false).

**Diagnostics** — how much of what the process did is written down, which is not a
setting about what Foyer *does*: `FOYER_LOG_LEVEL` (`DEBUG`, `INFO`, `WARN`,
`ERROR`; unset, and anything else, is `INFO`). It is a variable rather than a console
screen for one reason, and the reason is the rule above the table: **the console is
one of the things that can be broken.** A control plane so noisy it fills its own
log, or so quiet that the failure is invisible, is diagnosable only from outside it,
and the operator standing in front of a console that will not load is exactly the
person who needs this. Nothing about the product changes when it is set: the same
requests are authorised, the same settings apply, the same rows are written.

`INFO` is the default. `DEBUG` additionally records one line per request on the guest
portal and the console, which is worth knowing before setting it: the Captive
Network Assistant probes the portal repeatedly while a guest reads a page, and
`deploy/compose.yaml` bounds each container's log at 50 MB (`x-logging`). The Policy
API logs at `INFO` either way — an authorization decision is the product, and it is
a handful of lines a minute.

Every line is a JSON object, and every line written while serving a request carries
that request's `request_id`, which is also returned in the `X-Request-Id` response
header so a browser in front of a failure can be quoted for it. Two endpoints answer
questions about the process, and they are different questions: `/healthz` says
whether it is **alive** and consults nothing, and `/readyz` says whether it can
**serve** — it checks that PostgreSQL is reachable and that the stored network
settings are in force, and `deploy/compose.yaml`'s healthcheck probes that one.
`/metrics` is Prometheus-format and is on the Policy API listener alone, which is the
one `compose.yaml` does not publish. `internal/obs`'s CONTRACT says why each of
those is where it is.

**Bootstrap values of a console setting** — the value an installation starts with,
until an administrator saves one in the console, which then wins and is labelled
*Set here* (the installation's is *From the installation*):
`FOYER_PORTAL_URL`, `FOYER_VENUE_URL`, `FOYER_PORTAL_TLS_CERT`/`_KEY`. The last pair
is also where an automated (ACME) renewal writes, which is the one reason to keep
using it; [`../portal-https.md`](../portal-https.md) says how the two interact.

## Commands, and what they are for

`foyerctl` is inside the `foyer` container and is **not** how an operator configures
Foyer. It is for install, upgrade and support:

- `foyerctl migrate` — run by `deploy/up.sh` and `deploy/upgrade.sh`, never by hand
  except to read `--status` or roll back during a failed upgrade (`deploy/README.md`,
  *Upgrading*).
- `foyerctl create-operator` — the first administrator is created in the console
  with the installer's one-time setup code, and later ones under **Operators**. This
  command stays as the break-glass path for an install whose only administrator is
  locked out, which is the same place Grafana
  keeps `grafana cli admin reset-admin-password`.
- `foyerctl apply-raddb` — the gated FreeRADIUS configuration swap that the
  deployment's compose file and the lab run; it reads its probe client's shared
  secret from `FOYER_PROBE_SECRET` so that the secret is not on a command line.
- `foyerctl doctor`, `support-bundle`, `probe-unifi`, `sync-nas` — diagnostics for
  support (`probe-unifi` reads the controller credential it is probing with from
  `FOYER_UNIFI_API_KEY`/`FOYER_UNIFI_PASSWORD`, so that it is not on a command line).
  `sync-nas` is not a setup step: the control plane syncs the RADIUS client table
  itself.

The development and test variables — `FOYER_LAB_*`, `FOYER_MOCK_*`,
`FOYER_REQUIRE_WEBUI`, `FOYER_POLICYSTUB`, `FOYER_DIFF_PORT`, the `web/e2e` URL
overrides, `deploy/verify.sh --remote`'s `FOYER_HOST`/`FOYER_USER`/`FOYER_SSH_KEY`,
the image variables `deploy/images.sh` derives, and the installer scripts' own inputs
(`FOYER_ROOT`, `FOYER_REV`, `FOYER_BUILD_LOCALLY`, `FOYER_ALLOW_UNPINNED`) — are for
people working on or installing Foyer, not for people running it.

## Documents that say otherwise

Where any other document tells an operator to set a variable, this page supersedes
it. A document that tells an operator to set a `FOYER_*` variable or run `foyerctl`
to change behaviour is a defect in that document — please report it.
`tools/deploycheck` checks the operator documents for exactly that.
