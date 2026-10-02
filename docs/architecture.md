# Foyer architecture

This document describes what Foyer is, how its parts fit together, and the security
model they implement. Section numbers are stable — code and other documents cite them
(`docs/architecture.md §8.10`). The rules contributors follow when changing each area
are in [`docs/rules/`](rules/); what the protocols and vendors actually deliver, with
evidence, is in [`docs/protocol-reality.md`](protocol-reality.md).

---

## 1. What Foyer is

Foyer is an open-source **network access platform**: a captive portal, a RADIUS
server, and an admin UI shipped as one product.

Two audiences, one install, one identity store:

- **Guest access** — cafés, hotels, schools, factories, events. Vouchers, local
  accounts, phone sign-in by one-time code, click-through terms.
- **Corporate access** — 802.1X/EAP with dynamic VLAN assignment for employee
  devices.

Doing both from one install, against one identity store, is the product.

---

## 2. Why FreeRADIUS, and not a RADIUS server of our own

What Foyer adds over running FreeRADIUS directly:

1. **`raddb/` is generated from the database and never hand-edited.** Foyer
   regenerates it from live state, validates it, and swaps it atomically. The operator
   never edits FreeRADIUS, and **secure configuration becomes the only reachable
   state** (§8.4).
2. **One identity store serving both the guest portal and 802.1X**, with the
   credential-format consequences made explicit rather than discovered (§8.2).
3. **A measured install**: *a stranger installs it and a phone gets online* is a
   release criterion (§12), not a claim.

**802.1X/EAP is why Foyer does not implement RADIUS itself.** authentik implemented
EAP in Go over several years; what it supports is **EAP-TLS and PAP** — not PEAP,
not TTLS, not MSCHAPv2 — and its stated reason is that password authentication works
only with PAP because other methods require reversible hashes (§8.2). The dominant Go
RADIUS library, `layeh.com/radius`, implements RFC 2865/2866 with no EAP methods.
hostapd has a mature EAP server, but its RADIUS backend is a flat `eap_user` file or
SQLite with no policy language, no REST hook and no CoA origination; it cannot express
vouchers, quotas or database-driven policy. PacketFence ships a Go RADIUS
*accounting* server (`pfacct`) and leaves EAP to FreeRADIUS — the same conclusion.

**The cost: no single binary.** Foyer is a container set. In exchange, EAP is
FreeRADIUS's mature implementation rather than a new one. The generator emits
**EAP-TLS** today; EAP-TTLS/PAP is planned, and PEAP-MSCHAPv2 is not generated (§8.2).

---

## 3. Key decisions

| # | Decision | Rationale |
|---|---|---|
| D1 | **Scope: full platform** | Portal + RADIUS + admin + vouchers as one product. |
| D2 | **FreeRADIUS 3.2.x embedded; Go generates its config** | FreeRADIUS 4.0-alpha implements **no PEAP, TTLS, EAP-FAST or TEAP**, and §2 rests on a mature EAP implementation. |
| D3 | **`rlm_rest` for policy, `rlm_sql` for accounting** | One synchronous path and no extra daemon, and it keeps FreeRADIUS's stock `radacct` schema. The known escape hatch at scale is PacketFence's: move accounting out of the request path into an out-of-band daemon. |
| D4 | **Go + TypeScript + PostgreSQL** | Go for the control plane; Preact for the portal and React for the console (the portal's byte budget decides the split — [`rules/frontend.md`](rules/frontend.md)). |
| D5 | **Apache-2.0, fully open. No paid edition, no feature behind a licence check.** Contributions under **DCO**. | §9. |
| D6 | **NAS support: UniFi, MikroTik, Cisco and Aruba; Ruckus and Omada planned** | MikroTik's hotspot requires an external RADIUS server, so the demand is structural; Ruckus is a controller-API vendor, not a CoA one (§6). Vendor attribute profiles are not yet selected per NAS: every NAS gets the RFC-only profile today (§4). |
| D7 | **Login methods: voucher, local account, phone code, click-through. No social login.** | §13. |
| D8 | **Contract first** | The OpenAPI document is defined with the frontend and the backend is generated against it (§11). |

---

## 4. Architecture

```
                      ┌───────────────── control plane (Go) ──────────────────┐
   AP / Switch        │                                                        │
   (NAS)              │  ┌──────────┐  ┌──────────┐  ┌────────────────────┐  │
      │               │  │ Policy   │  │ Portal   │  │  raddb generator   │  │
      │   RADIUS      │  │ API      │  │ API      │  │  (DB → config)     │  │
      ├──auth/acct───▶│  └────▲─────┘  └────▲─────┘  └─────────┬──────────┘  │
      │  :1812/:1813  │       │ +Vendor     │                   │             │
      │               └───────┼──Profile────┼───────────────────┼─────────────┘
      │                       │ rlm_rest    │                   │ blue/green
      │               ┌───────┴───────┐     │          ┌────────▼─────────┐
      └──────────────▶│  FreeRADIUS   │     │          │  raddb.next →    │
                      │    3.2.x      │─────┼─────────▶│  raddb (symlink) │
      ◀──CoA/API──────│  EAP/802.1X   │     │          └──────────────────┘
                      └───────┬───────┘     │
                              │ rlm_sql     │          NAS clients are read
                              │ accounting  │          from SQL, not raddb
                      ┌───────▼─────────────▼──────┐   (read_clients = yes)
                      │        PostgreSQL          │
                      └────────────────────────────┘
```

### Component responsibilities

| Component | Does | Does not |
|---|---|---|
| **Policy API** (Go, `internal/aaa`) | Answers "may this identity onto the network, with what limits?" Renders the answer through a `VendorProfile`. Today that is the RFC-only profile for every NAS (`aaa.NewEngine`); selecting a vendor's profile per NAS is planned. | Speak the RADIUS wire protocol. Hold or return credentials (§8.3). |
| **Portal API + web** (`internal/portal`, `web/portal`) | Guest sign-in flows, voucher redemption. Creates *grants*. | Decide policy directly. Trust NAS-supplied parameters (§8.5). |
| **raddb generator** (`internal/raddb`) | Renders FreeRADIUS config from database state; validates; hands it to a supervisor that swaps blue/green. | Run per request. **Handle NAS client definitions** — those come from SQL. |
| **NASDriver** (`internal/nas`) | Out-of-band session enforcement: verify presence, authorize, revoke, reauthorize. | Know about portal flows or identity. |
| **VendorProfile** (`internal/nas`) | Maps an abstract decision (`Captive{PortalURL, PreAuthACL}` / `Authorized{VLAN, ACL, Bandwidth}`) onto vendor-specific attributes. | Perform out-of-band actions. |
| **Maintenance** (`foyer maintenance`, `internal/jobs`) | The stale-session reaper, expiry sweeps, retention and erasure — in its own container with its own database logins. | Hold the KEK. |
| **FreeRADIUS** | RADIUS wire protocol, EAP/802.1X state machine, TLS. | Hold business logic. |

### Table ownership

- **FreeRADIUS (`rlm_sql`) owns** `radacct` and `radpostauth`. Go treats them as
  read-only. The stock column set is kept verbatim — this is also the project's
  **exit hatch**: if Foyer is abandoned, the data is in a standard schema behind stock
  FreeRADIUS, and migrating *from* other FreeRADIUS-based tools is possible.
- **Go owns everything else**, and adds indexes, partitioning and retention to
  `radacct` (the column set is verbatim; the physical design is Foyer's).
- **Two audited exceptions**, each with its own PostgreSQL role: the stale-session
  reaper (§5.4) closes sessions orphaned by a NAS reboot with a four-column `UPDATE`;
  and retention/erasure (`internal/store/retention.go`) `DELETE`s rows whose retention
  period has passed or whose data subject asked to be erased. Neither may `INSERT`.
  [`rules/database.md`](rules/database.md) has the grants.

### FreeRADIUS version is a *dialect* contract, not an exact pin

The generator emits **3.2.x unlang** (`authorize { }`, `post-auth { }`); v4 uses
`recv Access-Request { }` and also changes attribute references, `update` sections,
module return codes and the dictionary model. Supporting v4 means a second emitter,
not an edit.

The contract is **dialect `3.2.x`, patch floor `>= 3.2.9`, ceiling `< 3.3`**, plus a
generated-config test suite run against the actually-installed binary. An equality
assertion would turn every upstream security fix into a Foyer outage. The exact
version is pinned only on the container image tag.

### Config application: SQL clients + blue/green

Two mechanisms, because a naive one causes outages.

**NAS changes never touch `raddb`.** FreeRADIUS reads clients from SQL
(`read_clients = yes`, `client_table`). Adding a NAS is a database write with zero
config churn. The naive alternative — regenerate and restart — **drops every
in-flight EAP conversation and the in-memory TLS session cache, forcing every
corporate laptop on site to re-authenticate whenever an admin adds an access point.**
SIGHUP does not reliably reload clients, listeners or virtual servers in 3.2.x, so
"just HUP it" is not available.

**Everything else swaps blue/green:** generate to `raddb.next` → `radiusd -XC` →
**start a second `radiusd` on alternate ports against it** → run `radclient` and
`eapol_test` smoke suites → swap and cut over, rolling back on any failure. In a
deployment the control plane writes each generated tree into a directory shared with
the FreeRADIUS container, whose supervisor validates, restarts, waits for *Ready to
process requests* and reports the outcome; the control plane never executes a command
string.

`radiusd -XC` alone is **necessary but not sufficient**, by FreeRADIUS's own
documentation — the `-C` checks are incomplete and some modules are skipped entirely.
It is the first of four gates, and the real load is carried by the second `radiusd`
and the smoke suites.

The generator classifies every change as `none` / `sql-only` / `restart` and shows
the operator which class they are about to trigger. That classification is a tested
contract in [`internal/raddb/CONTRACT.md`](../internal/raddb/CONTRACT.md).

---

## 5. Data flows

### 5.1 Guest portal — controller-API family (UniFi; Omada and Ruckus planned)

```
1. Client associates. Controller marks it unauthorized.
2. Client HTTP → controller redirects to Foyer portal
   (UniFi passes ap, id, t, url, ssid as UNSIGNED query parameters).
3. Client authenticates (voucher / account / phone code / click-through).
4. ⚠ Portal calls NASDriver.VerifyPresence() — confirms this MAC is really
   associated, on that AP, in that site, and still unauthorized.  (§8.5)
5. Grant {identity, mac, limits, expiry} created.
6. NASDriver authorizes via the controller API, passing hard limits where the
   controller can enforce them (UniFi: timeLimitMinutes, dataUsageLimitMBytes,
   rxRateLimitKbps, txRateLimitKbps).
7. Accounting → rlm_sql → radacct, IF the operator attached a RADIUS profile.
   Foyer asserts this at NAS registration and warns loudly if absent.  (§5.4)
```

### 5.2 Guest portal — CoA family (Cisco, Aruba, MikroTik)

```
1. Client associates. NAS does MAB → RADIUS Access-Request.
2. rlm_rest → Policy API: unknown MAC, no grant.
3. ⚠ Policy API returns captivity attributes SELECTED BY VendorProfile:
     Cisco  : url-redirect + url-redirect-acl        (Cisco AV-pairs)
     Aruba  : Aruba-User-Role / Aruba-Captive-Portal-URL
   These are NOT a standard. The profiles exist in internal/nas, but the
   engine renders the RFC-only profile for every NAS today; per-NAS profile
   selection is planned.
4. Client authenticates on the portal. VerifyPresence. Grant created.
5. NASDriver sends RFC 5176 CoA-Request to the NAS (port is PER-NAS —
   3799 is common, Cisco frequently uses 1700).
6. NAS re-issues Access-Request. Policy API finds the grant → Authorized attributes.
7. Revocation = Disconnect-Request. Handle CoA-NAK Error-Cause 503
   (Session Context Not Found) and 508 — otherwise a no-op reports success.
```

The two families diverge at step 5 — how a client is let in — **and at step 3** — how
a client is made captive. Step 3 lives in the Policy API, not in `NASDriver`, which is
why `VendorProfile` exists as a second abstraction (§6).

### 5.3 802.1X / EAP

```
1. Supplicant → NAS → RADIUS. FreeRADIUS runs the full EAP exchange.
2. Inner identity resolved → rlm_rest → Policy API for authorization + VLAN.
3. VLAN via Tunnel-Type / Tunnel-Medium-Type / Tunnel-Private-Group-Id,
   rendered by VendorProfile.
4. Accounting as above.
```

**Invariant:** authorization keys **only** on the *inner* identity. The outer identity
in PEAP/TTLS is client-supplied and arbitrary (`anonymous@realm`); any path that keys
off it lets an attacker choose their own authorization subject. Enforced by a contract
test in `lab/`.

### 5.4 Quota enforcement — and its honest limits

`radacct` byte counters advance only on Interim-Update or Stop. RFC 2869's
`Acct-Interim-Interval` has a widely-implemented **300-second floor**, so best-case
sampling is every 5 minutes — a worst-case overshoot of ~3.7 GB on a 100 Mbps link.
For a product selling 1 GB vouchers that is not a rounding error. The counters are
also 32-bit and wrap at 4 GiB; Gigawords carry the high bits (RFC 2866, RFC 2869).

On the controller-API path **there is no Access-Accept**, so Foyer has no channel to
request interim updates at all, and `radacct` rows exist only if the operator
separately attached an accounting profile.

Therefore:

- `Capabilities.HardQuota` — where the NAS can enforce limits itself (UniFi can),
  **delegate to it**; `radacct` is reporting, not enforcement.
- Where Foyer enforces, prefer NAS-enforced `Session-Timeout` / `Idle-Timeout` over
  polling; byte quota is a backstop.
- Emit `Acct-Interim-Interval` in every Access-Accept. Treat "no interim update within
  2× the interval" as a health alarm in the admin UI.
- Assert accounting is configured at NAS registration; fail loudly.
- **Stale-session reaper**: close sessions whose last update exceeds N × interval.
  Missing accounting is treated as session-end, never as unlimited.
- **State the granularity in the product UI.** "Sampled every N minutes; overshoot
  possible" — never imply real-time enforcement.

---

## 6. NASDriver and VendorProfile

Two abstractions, because there are two variable surfaces.

```go
// Out-of-band session enforcement.
type NASDriver interface {
    VerifyPresence(ctx context.Context, s Session) error   // §8.5 — mandatory
    Authorize(ctx context.Context, s Session, l Limits) error
    Revoke(ctx context.Context, s Session, r RevokeReason) error
    Reauthorize(ctx context.Context, s Session, l Limits) error
    Capabilities() Capabilities
}

// Session identification differs per platform; carry all of it.
type Session struct {
    AcctSessionID    string // Cisco fallback
    CallingStationID string // MAC — Aruba, UniFi
    AuditSessionID   string // Cisco IOS-XE: Cisco-AVPair audit-session-id
    NASIPAddress     netip.Addr
    NASIdentifier    string
    SiteID           string // UniFi
}

type Capabilities struct {
    LiveReauthorize bool       // change limits without disconnect
    WalledGarden    GardenKind // none | ip-range | fqdn-sni  — see §8.6
    HardQuota       bool       // NAS enforces quota itself → prefer it (§5.4)
    BandwidthLimits bool
    DynamicVLAN     bool       // property of the VendorProfile, not the driver
    RotateSecret    bool       // controller can push a new shared secret (§8.4)
}

// Renders an abstract decision into vendor attributes, chosen per NAS.
type VendorProfile interface {
    Captive(portalURL string, preAuthACL string) []radius.Attribute
    Authorized(vlan string, acl string, bw Bandwidth) []radius.Attribute
}
```

The code in `internal/nas` is authoritative; this sketch shows the shape.

### Driver families

| Driver | Enforcement mechanism |
|---|---|
| `UniFiDriver` | Controller API (Integration API on Network Application ≥ 9.1.105; the legacy `cmd/stamgr` → `authorize-guest` before it). **CoA-Disconnect available as a `Revoke()` fallback** — Ubiquiti documents port 3799, and PacketFence's UniFi driver registers both. |
| `CoADriver` (MikroTik, Cisco, Aruba) | RFC 5176 CoA, per-NAS port |
| Ruckus (planned; a `VendorProfile` exists) | **Northbound Interface JSON API** |
| Omada (planned) | Controller API |

**Ruckus is not a CoA vendor for captive portal.** SmartZone's external-portal model
is the Northbound Interface JSON API — the portal calls back into the controller with
JSON to authorize. PacketFence's documentation is explicit that for MAC-auth in
non-proxy mode the deauthentication method must be set to HTTP/HTTPS to use the
Northbound API *instead of* RADIUS Disconnect/CoA, because otherwise the code tries
RADIUS by default and fails to disconnect the user.

**UniFi can do CoA.** The accurate, narrower claim: **UniFi's captive-portal
*authorization* has no RADIUS equivalent.** Its revocation does, which gives `Revoke()`
a useful fallback against vendor API drift.

**What Foyer cannot configure:** `url-redirect-acl` names an ACL that must already
exist *on the Cisco NAS*, and `Filter-Id` names a filter list that must already exist
on the NAS. Foyer cannot generate either. "The operator configures Foyer, not
FreeRADIUS" holds; "the operator configures nothing" does not. Per-vendor NAS
configuration ships as tested artifacts in `lab/`, not as prose.

---

## 7. Captive Portal API (RFC 8908 / 8910) — a conditional enhancement

RFC 8908 requires the API be served over `https`, with a certificate carrying a
DNS-ID matching the provisioned hostname, and states that if the client cannot
validate that certificate it **MUST NOT proceed**. A box on a café LAN has no public
DNS name and cannot obtain a publicly-trusted certificate by default.

Therefore:

- **HTTP/DNS interception is the default path and always works.** RFC 8908 is a
  progressive enhancement that activates only when a valid certificate exists. Both
  paths are built and both are tested.
- Certificate provisioning is the operator's choice, made in the console: a
  real-domain certificate (ACME **DNS-01** resolving a public name to a LAN address is
  the mechanism Plex and Tailscale use) or bring-your-own-cert, with a clear statement
  of what is lost without one. [`docs/portal-https.md`](portal-https.md).
- Response `Content-Type` **MUST** be `application/captive+json` — the most common
  implementation error (RFC 8910's text says `application/capport+json`; errata EID
  6620 corrects it).
- Only `captive` is a required member. `user-portal-url`, `venue-info-url`,
  `seconds-remaining`, `bytes-remaining`, `can-extend-session` are optional — and
  clients ignore most of them, so no UI is built on them.
- The API endpoint and the certificate's OCSP/CRL endpoints must be inside the walled
  garden pre-auth, or the portal cannot be served at all.

RFC 8908 avoids the *interception* warning; it makes certificate requirements
**stricter**, not looser.

**Platform support:** iOS 14+ and macOS Big Sur+; Android 11+ (a subset); ChromeOS.
Windows does not implement it. [`protocol-reality.md`](protocol-reality.md) records
exactly which keys each client parses.

---

## 8. Security model

### 8.1 Threat model

Adversaries, named: a guest on the open SSID; an attacker in radio range; a malicious
NAS or on-path attacker between NAS and server; a compromised container on the compose
network; an Internet scanner reaching a published port; and — in multi-tenant
deployments — a hostile tenant.

### 8.2 Credential storage

Challenge-response methods force the server to hold recoverable material:

| Method | Server must store | argon2 possible? |
|---|---|---|
| PAP (portal, console) | anything | **Yes** |
| EAP-TTLS/PAP | anything `rlm_pap` can read | **No** — `rlm_pap` has no argon2 scheme |
| CHAP | **cleartext** | No |
| MSCHAPv2, PEAP-MSCHAPv2 | **NT hash** = unsalted MD4 | No |
| EAP-TLS | no password | N/A |

The NT hash is **password-equivalent** — it is the sole input to the MSCHAPv2
response, so a stolen hash authenticates directly without cracking. Because §1 puts
guest and corporate identities in one store, a stolen Foyer database is plausibly a
stolen *directory* credential set.

**Decisions:**

1. **CHAP is not supported.** Cleartext storage for a legacy checkbox is not worth it.
2. **EAP-TLS is the corporate default; EAP-TTLS/PAP is the planned password
   method.** TTLS/PAP gives a TLS tunnel and a verifiable inner password. Because
   FreeRADIUS's `rlm_pap` cannot verify argon2id, an identity opted in to RADIUS
   password authentication carries a **second, one-way bcrypt verifier** that
   FreeRADIUS reads from a minimally-privileged view; it is not password-equivalent
   and does not make MSCHAPv2 work.
3. **PEAP-MSCHAPv2 is never on by default.** If it is ever offered it is opt-in per
   identity, behind an explicit statement that it requires NT-hash storage, in a
   **separate table, envelope-encrypted under a key outside the database**.
4. **Admin and portal credentials are argon2id, always**, in a different store. They
   never need MSCHAPv2.
5. Stated in the README, so deployers are not surprised.

**What the generator emits today is EAP-TLS only.** `Generator.Validate` refuses
`ttls` and `peap` by name, with the reason: TTLS/PAP is not yet wired to the bcrypt
verifier, and MSCHAPv2 needs the NT hash in the clear at request time, which the
envelope-encrypted store deliberately cannot provide to `rlm_sql`.

PEAP-MSCHAPv2 is also offline-crackable if the supplicant does not validate the server
certificate — which is why supplicant profiles that pin the CA **and** the server name
matter. [`rules/security.md`](rules/security.md) §1 has the full rules.

### 8.3 The `rlm_rest` contract

- **Credentials never leave FreeRADIUS.** FreeRADIUS fetches verifiers via `rlm_sql`
  from a minimally-privileged view; `rlm_rest` carries **authorization decisions
  only**. Otherwise the Policy API would be an endpoint returning the NT hash for any
  username supplied. This extends "Go never sees the credential exchange" from EAP to
  *every* method. An allow says what it authorized (a voucher, or an account that must
  still pass `rlm_pap`); it is not itself an authentication.
- **It is not localhost.** FreeRADIUS and Go are separate containers on a shared
  bridge. **mTLS** between them, certificates issued at install. The Policy API refuses
  non-TLS on any non-loopback address — a startup assertion, not documentation.
- **`rlm_rest` is blocking.** FreeRADIUS 3.x uses a fixed thread pool and libcurl runs
  on the request thread; if Go stalls, all threads block and **Status-Server is blocked
  too, so the health check dies with the thing it checks.** Therefore:
  `connect_timeout` ≈ 0.5 s, `timeout` ≈ 1–2 s — both under the NAS retransmit interval
  (~3 s) — and the connection pool sized against the thread pool. An `rlm_cache` in
  front, keyed on `Calling-Station-Id` + NAS, is planned and not generated today.
- **Failure mode is a stated product decision, per site.** Default **fail closed** for
  802.1X, **configurable fail-open for guest** (a hotel and a factory want opposite
  things). Today the generator renders one failure model into `default` and refuses,
  at Validate, sites that do not agree on one. Routing each RADIUS client into a
  virtual server per distinct failure configuration is planned
  (`internal/raddb/config_failure.go` says what it waits on).
- A `lab/` test SIGSTOPs the Go process and asserts the defined behaviour.

### 8.4 RADIUS shared secrets and BlastRADIUS

**CVE-2024-3596 ("BlastRADIUS")** lets an on-path attacker forge an Access-Accept from
a genuine Access-Reject using MD5 chosen-prefix collisions. FreeRADIUS's default
`require_message_authenticator` is **`auto`** — which exists to avoid breaking legacy
gear, and therefore silently leaves the weakest devices unprotected.

**Because Foyer generates the configuration, the mitigation is Foyer's
responsibility, not the operator's.** A generating product can *force* secure
configuration where stock FreeRADIUS must stay backward-compatible.

- Emit `require_message_authenticator = yes` and `limit_proxy_state = yes` globally,
  in every network section and in the dynamic-clients site. (Not per client: once
  clients come from SQL there is no column for it, and a global `yes` cannot be
  weakened by a row.) A NAS that cannot comply is a **loud, per-NAS warning in the
  admin UI**, never a silent downgrade.
- Secrets: per-NAS, `crypto/rand`, **no default ever** (FreeRADIUS ships
  `testing123`), envelope-encrypted at rest, write-only in the admin API, redacted from
  logs and support bundles — `radiusd -X` output contains secrets in the clear.
- `RotateSecret` capability: for controller-managed NAS the driver can push the new
  secret, making rotation one click; elsewhere rotation is still offered, and the
  operator carries the new secret to the device.

### 8.5 The portal must not trust the NAS redirect

**UniFi passes `ap`, `id`, `t`, `url`, `ssid` as unsigned query parameters.** Anyone
can call the portal URL with values of their choosing.

Attacks: authorize an arbitrary MAC; farm grants for devices that never associated;
steer quota and billing onto a victim.

**Mitigation — `VerifyPresence` is a mandatory step and a driver obligation** (§5.1
step 4, §5.2 step 4): confirm the MAC is really associated, on the claimed AP, in the
claimed site, and still unauthorized. Where a vendor offers a signed payload, require
it. Bind the portal session cookie to the MAC and reject mid-flow changes. No grant is
created for a MAC not confirmed present by the driver in that request.

`VerifyPresence` proves a MAC is on the network, not that the caller *is* that device,
so anything that spends or changes a particular guest's state is keyed on what that
guest's own connection proves (the granted portal session, or the MAC together with
the source address it arrived from).

**Open redirect**: the NAS passes the original URL. A naive bounce is an open redirect
on a network where the user *expects* to be redirected and has just typed credentials.
Scheme allowlist, no protocol-relative or userinfo URLs, and a success page with the
destination as a link rather than an auto-redirect.

### 8.6 The walled garden is unauthenticated egress

The garden is a set of destinations any stranger in radio range can reach, with no
identity established.

- Enforce by **FQDN/SNI where supported, never by CDN IP ranges** — "allow the OAuth
  provider" degrades to "allow a general-purpose HTTPS tunnel" because modern CDNs host
  arbitrary customers. `Capabilities.WalledGarden` is a three-valued `GardenKind`, and
  an IP-range-only driver is documented as materially weaker.
- **Quota the unauthenticated state**: per-MAC byte cap, rate limit, and a time limit
  before teardown. Rate limits are keyed on what the caller cannot type — the
  connection source before presence is verified, the verified MAC on that source after.
- Restrict pre-auth DNS to the local resolver with response-size and rate limits (DNS
  tunnelling for free internet is decades old and still works).
- No operator-supplied wildcard TLDs.
- **Required garden contents:** the portal's own FQDN, its certificate chain's
  OCSP/CRL endpoints, and the RFC 8908 API endpoint.

### 8.7 MAC identity is weak — say so

A MAC-bound grant is only as strong as MAC authenticity, and 802.11 addresses are
cleartext regardless of WPA2/WPA3. iOS 14+ and Android 10+ present a per-SSID private
MAC by default (Android's is persistent on a network that has shown a captive portal,
so "remember this device" remains viable as a pseudonym).

Position: bind grants to *identity*, with MAC as a hint; detect the
locally-administered bit; concurrency limits per grant. **The captive portal is a
billing and consent boundary, not a security boundary** — the documentation says so
rather than letting an operator discover it after selling access.

### 8.8 Admin plane

- **No static credentials anywhere in the repository**, and **no administrator
  password at all**: the installer generates the database passwords, signing keys and
  RADIUS secrets, and opens a one-time **setup code** with which the operator creates
  the first administrator with a password of their own. The service **refuses to
  start** if a known-default value is present.
- Admin UI and Policy API on **separate listeners** from the guest portal. The console
  is HTTPS on its own self-signed certificate, generated by the application.
- Built-in roles (administrator and staff), so a shift worker issuing vouchers is not
  handed the account that can read every shared secret. Admin MFA is not implemented
  yet.
- Append-only **admin audit log**: secret rotation, grant issuance, NAS registration,
  exports and erasures. An operation that cannot be undone writes its audit entry in
  the same transaction, fail-closed.
- Sign-in lockout split by device, so a stranger flooding an account does not lock its
  real user out.
- CSRF (including on downloads), CSP, secure/httpOnly/SameSite cookies, no stack traces
  in API errors.

### 8.9 PKI

For EAP-TLS the operator **uploads** the server certificate and key and the external CA
that issues client certificates (`putEapCertificate`); Foyer does not act as a CA
issuing client certificates (§13).
Wherever a CA key exists it is encrypted under a key **not in the database**, never
exportable through the API, and access is logged. **Never ship a CA in the image** — a
fixed key would mean every Foyer deployment on earth shares one private key; refuse to
start if a development key is detected. An expired CRL fails *all* EAP-TLS, so CRLs are
regenerated well before `nextUpdate` with a staleness alarm; revocation triggers a
Disconnect-Request, because otherwise it takes effect only at the next authentication
and an 802.1X session can run for days.

### 8.10 Multi-tenancy seams exist from migration 00001

`tenant_id` on **every Go-owned table from migration `00001`**, single-tenant installs
pinned to one row, plus **PostgreSQL Row-Level Security** (forced) so isolation is
enforced by the database rather than by contributor discipline. Without this, every
contribution writes queries with no tenant predicate, and each missed predicate is a
cross-tenant leak no review can catch at scale. The *seam* has to exist before the
queries are written.

`radacct` has no tenant column and never will (stock schema). Attribution is
**NAS-derived**, which requires the invariant that **no two tenants may claim
overlapping client addresses or the same NAS identifier** — FreeRADIUS's client list is
one flat namespace. The `nas` table FreeRADIUS reads therefore has no row-level policy
(FreeRADIUS never sets the tenant), and the projection refuses a conflicting address by
naming the tenant that holds it.

---

## 9. Licence and contribution

**Everything is Apache-2.0 and free. There is no paid edition, no paid tier, and no
feature behind a licence check.** Multi-tenancy, HA, SSO, RBAC, retention and reporting
are all simply features. Security features in particular — RBAC, the audit log,
RadSec, retention configuration, a second RADIUS node — are never behind a boundary,
because a paywalled security feature becomes a permanent security downgrade for
everyone who did not pay.

Apache-2.0 carries an **explicit patent grant** with a litigation-termination clause,
and publishing the source is what lets a security team audit the software they place
at their authentication boundary.

**Contributions use the DCO, not a CLA**: a `Signed-off-by:` line certifying you wrote
the change and may submit it. Contributors keep their copyright; inbound equals
outbound. See [`CONTRIBUTING.md`](../CONTRIBUTING.md).

---

## 10. Testing

`lab/` is a docker-compose lab with real FreeRADIUS, a simulated NAS speaking RADIUS
and answering CoA on a configurable port, and a fake UniFi controller serving recorded
fixtures.

| Tier | What | Where |
|---|---|---|
| 1 | Go unit tests; Postgres via testcontainers; frontend unit tests | CI, laptop |
| 2 | Real FreeRADIUS via `radclient` **and `eapol_test`**; `radiusd -XC`; snapshot-tested generated config; blue/green cutover | CI, laptop |
| 3 | Portal + admin E2E (Playwright), with axe | CI, laptop |
| 4 | Hardware acceptance **plus a device matrix**: iOS, Android, Windows, macOS | Manual, release gate |

**`eapol_test`** (from wpa_supplicant) links an EAP peer and a RADIUS client into one
binary specifically to test EAP methods without an access point. `radclient` and
`radtest` do not speak EAP; a project whose rationale is "EAP is too hard to implement
ourselves" needs EAP in CI.

**What the lab cannot do.** `lab/` prevents regressions in what has been learned on
hardware; it cannot teach what hardware does. A hand-written NAS mock encodes its
author's assumptions. Faithfully *rejecting* a CoA is the hard part: real gear NAKs on
session-identification mismatches, differs per vendor, is picky about source IP, and
returns Error-Cause codes. Tier 3 **cannot emulate the iOS Captive Network Assistant**,
so portal E2E can be green while real phones fail — which is why tier 4 is a device
matrix.

**Security tests are named, not generic:** credential-storage invariants, garden-escape
tests, the inner/outer identity test (§5.3), a `require_message_authenticator`
generator snapshot test, Policy API unavailability (SIGSTOP), `VerifyPresence`, and
injection payloads on every validator. [`rules/testing.md`](rules/testing.md).

---

## 11. Contract first

The OpenAPI document is the single definition of the API, shared by the frontend and
the backend: `web/contract/openapi.yaml` defines every operation, a stateful mock serves the
frontends from it and validates every response against it, and the Go server code is
generated from it — a handler for an operation that does not exist fails the build,
and so does an operation with no handler.

This fits a domain where the value is in operability rather than protocol, with one
standing danger: a UI designed against mock data will promise things RADIUS cannot
deliver — a live bandwidth graph when interim updates are 5 minutes apart and often
disabled (§5.4); "edit session" affordances on FreeRADIUS-owned tables (§4); controls
that must disappear per driver (§6 `Capabilities`). Hence the rule that a screen may
display only data [`protocol-reality.md`](protocol-reality.md) shows exists, and
**fixtures that are deliberately ugly**: sessions with no Acct-Stop that stay "online"
forever, interim updates 20 minutes stale, randomised MACs with no hostname, duplicate
sessions for one MAC, clock skew, an unreachable NAS, an expired API key, vouchers
expired, exhausted and revoked mid-session.

The guest portal's hardest constraints are client-environment constraints, frozen
from the start: no CDN fonts, no external scripts, no third-party analytics — **the
portal must render before the client has internet** — and i18n from the first commit
(English and Vietnamese).

---

## 12. First release

> **First moment of user value:** *an operator with one access point creates a
> voucher, and a phone gets online.* Everything not on the path to that sentence is
> justified against it or cut.

**A stranger installs it and a phone gets online** is a release criterion, timed on a
fresh host. `docker compose up` was never the hard part; the minutes that matter are
*after* the container starts: configuring the AP, obtaining a certificate the guest's
phone trusts, creating a controller API key, making DNS resolve pre-auth. The
first-run wizard, `foyerctl doctor`, the portal TLS answer and the installer exist for
those minutes.

**The upgrade path is part of the first release**, not something added later: the
first person to install must be able to upgrade. Schema changes are numbered
migrations applied by `foyerctl migrate`; `deploy/upgrade.sh` backs up, migrates with
the application stopped, and verifies ([`deploy/README.md`](../deploy/README.md)).

Diagnosability is the support strategy:

- **`foyerctl support-bundle`** — generated raddb, a `radiusd -X` excerpt with secrets
  redacted (§8.4), NAS capability probe results, recent auth failures *with reason
  codes*.
- **`foyerctl doctor`** — probes NAS reachability, shared secret, walled garden,
  certificate validity, clock skew.
- Error messages that name the fix, and an admin screen that answers *why can't this
  guest get online?*

---

## 13. Non-goals for v1

- Writing a RADIUS or EAP implementation.
- FreeRADIUS 4 (no PEAP/TTLS/EAP-FAST/TEAP).
- **Social login.** Facebook Wi-Fi was discontinued in 2023 and Meraki, NETGEAR and HPE
  ended their integrations; generic OAuth remains, but the **CNA has no persistent
  cookies and cannot open new windows**, which is exactly what OAuth redirect flows
  need, and it needs per-driver walled-garden automation across every driver.
- **Foyer acting as a certificate authority.** v1 imports the EAP-TLS server
  certificate and the external CA that issues client certificates. Issuing client certificates requires device
  onboarding — SCEP, Apple `.mobileconfig`, Windows autoenrollment, Android — which is
  where PacketFence and ClearPass spend most of their engineering, and enterprises that
  want EAP-TLS almost always have a CA already.
- **.deb/.rpm packages and Helm.** Distribution FreeRADIUS versions lag (most
  distributions ship a version outside `>= 3.2.9, < 3.3`), so a native package either
  fails the dialect contract or weakens it. Container-only for v1.
- **Reverse CoA.** Requires the non-default `WITH_COA_TUNNEL` build flag, is
  beta-quality, implements an Internet-Draft rather than an RFC, and nothing in this
  design uses it.
- **Device fingerprinting** (DHCP or otherwise), full NAC, being a general-purpose
  IdP.

---

## Object model: controller, RADIUS client, access point

What an operator thinks of as "an access point" is **three different objects** with
different cardinalities, identifiers, lifecycles and owners. Fusing them into one
record makes a registration flow that cannot work on controller-managed hardware.

| Object | What it is | Identified by | Who creates it |
|---|---|---|---|
| **Controller** | The management plane Foyer holds credentials for. Authorizes, verifies presence, revokes, and is the only thing that knows what access points exist. | operator-supplied URL | the operator, once |
| **RADIUS client** | A row FreeRADIUS matches an Access-Request against: an address or CIDR, a shared secret. | IP address or CIDR | **derived** from the controller's device list, or entered for a CoA-family NAS |
| **Access point** | A radio a guest is standing under. Appears in the NAS redirect and in session rows. Foyer configures nothing on it. | **MAC address** | **discovered** from the controller |

The precise claim: **the object an operator possesses and configures is the
controller; the access point is discovered from it; and the RADIUS client set is the
union of the addresses the access points speak from.** All three exist; only one is
typed in.

Consequences that are invisible while they are one object:

- **Their counts differ.** One controller, one shared secret, twelve RADIUS client
  addresses, twelve access points.
- **Their identifiers differ.** The redirect carries the access point's **device MAC**
  (not the BSSID — measured), which only the controller can resolve.
- **Their failure states differ.** "Unreachable" means at least three things: the
  controller did not answer; the controller answered and says the AP is offline; the
  RADIUS client has not been heard from.

The evidence, all public:

- **UniFi's External Hotspot API** is entirely site-scoped on a controller; the access
  point appears once, as a MAC in a query string. PacketFence's UniFi module describes
  the device type as the controller, **probes** the controller flavour (UniFi OS vs.
  standalone) rather than asking, and discovers access points from the controller
  (`populateAccessPointMACIP()` caches `mac → ip` for every AP and every BSSID).
- **The shared secret is one per RADIUS profile** on the controller, while **every
  access point is its own RADIUS client** — PacketFence sends Disconnect-Requests to
  the AP's own address. So `clients.conf` needs N addresses (or a CIDR) against
  **one** secret. HPE Aruba documents `dynamic-radius-proxy` as *an alternative to
  adding all APs as NAS clients*, an operator toggle — so the same hardware yields one
  RADIUS client or twelve, and the client set must be discovered and reconciled, with
  an unmatched source address treated as a finding.
- **Omada** is a controller too, and needs two values it cannot derive: the
  `CONTROLLER_ID` from the controller's own URL, and a hotspot-operator account
  (not the controller admin account).

In the schema this is `controllers` → `shared_secrets` → `radius_clients` →
`access_points` (migration `00004`; `00012` adds the controller to
`portal_sessions`). The contract's `Nas` is a **read
model** — one row per device, joining RADIUS client and access point — and rotating a
secret acts on the RADIUS client set, not on one device. Access points are discovered,
never typed; one that stops appearing is marked `missing`, not deleted, because its MAC
resolves past sessions. `Capabilities` lives only on the RADIUS client — a second copy
elsewhere would be a second source of truth for a value that decides whether a control
exists at all. Each driver declares its shape:

| Driver | Controller | RADIUS clients | Access points | Authorize | Revoke |
|---|---|---|---|---|---|
| `unifi` | **required** | discovered | discovered | controller API | controller API, CoA fallback |
| `omada` (planned) | **required** | discovered | discovered | controller API | controller API |
| `ruckus` (planned) | **required** (SmartZone) | discovered | discovered | Northbound JSON API | Northbound JSON API |
| `mikrotik` | none — the router is its own | manual, one | implicit-single | CoA | CoA |
| `coa` (Cisco WLC, Aruba) | none | manual — one controller, or many APs | implicit-single | CoA | CoA |

On the UniFi controller path, with no `radacct` row at all, Foyer can still show **who
is authorized, since when, until when, and under what limit**, and bytes used from the
client's detail endpoint ([`protocol-reality.md`](protocol-reality.md)).
