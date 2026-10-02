# Foyer

**An open-source captive portal and RADIUS platform.** Guest Wi-Fi and corporate
802.1X from one install, against one identity store.

> **Status: pre-1.0.** Foyer installs from [`deploy/`](deploy/README.md) and has been
> run against real UniFi equipment: a controller registered and probed, its access
> points discovered and enforced as RADIUS clients, and a phone's captive-portal sheet
> redirected to the Foyer portal by a real access point. It has not yet been
> field-tested on a wide range of hardware or at real venues. The other drivers are
> implemented against vendor documentation and tested in `lab/`, not on the vendors'
> equipment; of the 802.1X methods, EAP-TLS is the one exercised against real
> FreeRADIUS. [`docs/protocol-reality.md`](docs/protocol-reality.md) records what has
> been measured, what is documented, and what is still unverified. Expect breaking
> changes before 1.0.

---

## What it is

A café needs vouchers and a login page. A factory needs 802.1X with dynamic VLAN
assignment. Most tools do one or the other; running both usually means running two
systems with two user databases.

Foyer does both, and it does the part everyone else leaves to you:
**FreeRADIUS is an implementation detail.** Its entire configuration is generated
from the database, validated, and swapped atomically. You never edit `raddb/`.
You may never need to know it is there.

## Why generated config matters more than convenience

Operator misconfiguration is the dominant real-world RADIUS weakness — a wrong
`clients.conf`, CRL checking left off, PEAP running on the shipped bootstrap
certificates.

A generating product can *force* correct configuration where stock FreeRADIUS
must stay backward-compatible. The clearest example: FreeRADIUS defaults
`require_message_authenticator` to `auto` so it does not break legacy hardware —
which means the weakest devices on your network are the ones left exposed to
[BlastRADIUS (CVE-2024-3596)](https://www.freeradius.org/vul_notifications/2024/07/09/blastradius.html).
Foyer emits `yes` everywhere and tells you loudly, per device, if one refuses.

## What ships

- Guest portal — vouchers, local accounts, phone sign-in by one-time code (eSMS or
  Zalo), click-through terms, and a page builder so a venue can make the page its own
- Drivers for UniFi (controller API), and MikroTik, Cisco and Aruba (RFC 5176 CoA);
  Omada and Ruckus are planned. Vendor attributes are not yet selected per device
  (see the limitations below)
- RFC 8908/8910 Captive Portal API, with HTTP interception as the always-works
  fallback
- Admin console — controllers and access points, vouchers, live sessions, accounts,
  guest data, retention and erasure, **in English and Vietnamese**; every setting is
  made here, never in a config file
- A first-run wizard and `foyerctl doctor`, so setup is not an exercise in
  reading someone else's runbook
- 802.1X with EAP-TLS
- An installer, upgrades, scheduled backups, a restore that has been run, and a
  `verify.sh` that tells you whether the machine still matches the repository

**Deliberately not in v1:** social login, Foyer acting as a certificate
authority, and full NAC. [`docs/architecture.md`](docs/architecture.md) §13 gives
the reason for each.

## Honest limitations

We would rather you read these here than discover them in production.

- **Quota is sampled, not real-time.** RADIUS accounting arrives on an interval
  with a 300-second floor, and some hardware does not send it at all. Where the
  access point can enforce limits itself, Foyer delegates to it. Where it cannot,
  the UI shows you the sampling interval instead of pretending.
- **A captive portal is a billing and consent boundary, not a security
  boundary.** Access is bound to a MAC address, which is transmitted in the clear
  and can be spoofed. For real security, use 802.1X.
- **RFC 8908 needs a certificate your guests' devices trust.** On a LAN appliance
  with no public DNS name, that is not automatic. Foyer documents a path and falls
  back to interception, which always works.
- **EAP-TLS is the only 802.1X method generated.** EAP-TTLS/PAP is planned: FreeRADIUS
  cannot verify the argon2 hashes Foyer stores, so it needs a separate, opted-in
  bcrypt verifier. PEAP-MSCHAPv2 is not generated; it requires storing a
  password-equivalent NT hash and would only ever be an explicit per-identity opt-in.
  [`docs/rules/security.md`](docs/rules/security.md) §1 explains the trade-off.
- **Vendor attribute profiles are not yet selected per NAS.** The Policy API answers
  every NAS with the RFC-standard attributes; the Cisco and Aruba profiles (Cisco's
  `url-redirect` pair, Aruba's role and captive-portal URL) exist but are not chosen
  per device yet, and Cisco's two repeated `Cisco-AVPair` values cannot both travel
  the `rlm_rest` path today ([`docs/protocol-reality.md`](docs/protocol-reality.md)).

## Install

On a clean Ubuntu 24.04 host:

```bash
git clone <this repository> foyer && cd foyer
sudo bash deploy/install.sh
```

The installer generates every secret on the host, starts the stack, and prints a
one-time **console setup code** — Foyer ships no default password. Open
`https://<host>:8443`, enter the code, and create your administrator.
[`deploy/README.md`](deploy/README.md) covers first run, upgrades, backups and
restore, and [`docs/operator/`](docs/operator/configuration.md) what is configured
where.

## Development

Requires Go 1.26+, Node 20+ with pnpm, and Docker.

```bash
make lab-up      # real FreeRADIUS + Postgres + the Go control plane, no hardware
make lab-nas-auth  # a real Access-Request as a NAS, accepted and refused
make lab-test    # tier 2, against a real FreeRADIUS 3.2.x — needs Docker
go test ./...
cd web && pnpm install && pnpm dev   # mock + portal + admin console
```

`make lab-test` is not a second set of tests. `lab/eap` and `lab/policy` are
ordinary Go packages, so `go test ./...` already names them — but without
Docker they *skip*, and a skipped EAP suite reads exactly like a passing one.
The target supplies what they skip without (a daemon, a FreeRADIUS image inside
the 3.2.x dialect the generator emits for, `FOYER_LAB_E2E`, a generated
`lab/raddb`), and then fails if any required tier-2 test reported anything other
than a pass. On a machine that cannot run them it says so and exits
non-zero rather than going quietly green.

`lab/` exists because the chronic problem with RADIUS software is that you cannot
try it without hardware. It will not teach you what real equipment does — only
hardware does that — but it means you can contribute without owning an access
point.

**Documentation for contributors:**

- [`docs/architecture.md`](docs/architecture.md) — components, data flows, the
  security model, the object model
- [`docs/rules/`](docs/rules/) — the rules for each area of the code, and why
- [`docs/protocol-reality.md`](docs/protocol-reality.md) — what RADIUS, the vendors
  and the phones actually do, with the evidence
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — how to send a change

## Licence and contributing

**Apache-2.0.** Free to use, modify and redistribute, including commercially,
with an explicit patent grant.

There is no paid tier, no enterprise edition, and no feature behind a licence
check. If you need help beyond the documentation, commercial support is
available — but the software is complete without it.

Foyer is maintained by **Nguyễn Trung Kiên** (kiennt048@gmail.com), who holds the
copyright stated in [NOTICE](NOTICE).

Contributions use the [DCO](CONTRIBUTING.md): sign your commits with `git commit
-s`. No CLA. Please read the [Code of Conduct](CODE_OF_CONDUCT.md).

Security issues: please read [SECURITY.md](SECURITY.md) rather than opening a
public issue.
