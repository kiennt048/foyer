# Protocol reality

**What RADIUS, the vendors, and the accounting layer actually deliver — and how
fresh it is.**

This file exists because a frontend built against imagination promises things the
protocol cannot keep. **Nothing may be displayed in a Foyer UI unless it appears
here or in a cited RFC/vendor document.** If a screen needs a field that is not
recorded below, the answer is to measure it, not to mock it.

Each row is one of:

- **MEASURED** — observed on real equipment or a real binary, with what was used.
- **DOCUMENTED** — from an RFC or vendor documentation, with the source.
- **UNVERIFIED** — believed, not confirmed. **Not usable as a UI guarantee.**

Measurements recorded here were taken against FreeRADIUS 3.2.10 in `lab/`, a UniFi
controller and access point, and an iPhone joining an open guest SSID, unless a row
says otherwise. When equipment
teaches something new, the deliverable is a new `lab/` test plus a line in this file.

---

## Baseline facts

Four facts every other screen and driver depends on.

| Fact to establish | How | Status |
|---|---|---|
| FreeRADIUS + `rlm_rest` returns Access-Accept from the Go policy engine | `lab/`, `radclient` | **MEASURED on FreeRADIUS 3.2.10.** `rlm_rest` calls the Go policy engine, and the reply map reaches the packet. A denial answers `Reply-Message = "denied by policy"`; with the control plane down the request fails closed with `Reply-Message = "policy engine unavailable"`. |
| A real UniFi controller accepts guest authorization with an API key | real controller | **MEASURED.** The Integration API (`/proxy/network/integration/v1` on UniFi OS) accepts `X-API-KEY` (case-insensitive; `Authorization: Bearer` is refused `401`) and authorizes a guest with `AUTHORIZE_GUEST_ACCESS`. See *The controller is the object* below. |
| `radacct` rows appear, and **which columns are actually populated** | `lab/` + `radclient` / real NAS | **SPLIT.** `radpostauth`: **MEASURED on 3.2.10** — the running server's own trace shows the INSERT carrying both station ids and no `pass`. `radacct` Start/Interim/Stop: **MEASURED on 3.0.27 only**, storing `acctsessiontime`, `acctinputoctets`, `acctoutputoctets`, `callingstationid`, `calledstationid`, `framedipaddress`, `acctterminatecause`; **not re-taken on 3.2.x**. In every case measured with `radclient`, never a NAS — *which columns a real device populates* is still open. |
| The real redirect parameters each NAS sends | packet capture | **UniFi: MEASURED** (below). **Omada: DOCUMENTED** from vendor sources, both paths. Whether other devices send anything beyond what is documented, and how the encoded `ec` variant behaves, is open. |

### Which FreeRADIUS a distribution packages

**Every alpine release from 3.19 to 3.23 packages FreeRADIUS `3.0.27`**, not 3.2.x —
a different major version from the one `internal/raddb` emits for and the dialect
contract in [`docs/rules/radius.md`](rules/radius.md) asserts. 3.0 and 3.2 are close
enough that a configuration written for 3.2 mostly loads on 3.0, so a suite run
against the wrong major version can stay green while checking nothing
([`docs/architecture.md`](architecture.md) §4).

`radiusd -v` prints the version on the first line of its output. **Run it before
recording a measurement.**

| Distribution | FreeRADIUS | Standing |
|---|---|---|
| alpine 3.19 – 3.23 | 3.0.27 | MEASURED — `apk search -x freeradius` |
| Ubuntu 24.04 | 3.2.5 | MEASURED — `apt-cache policy freeradius` |
| Debian trixie | 3.2.7 | MEASURED — same |
| **Debian sid** | **3.2.10** | MEASURED — same; **the lab image** |
| InkBridge upstream repository | amd64 only, no arm64 index | MEASURED — Foyer builds on arm64 too |

Debian sid is the only distribution inside `>= 3.2.9, < 3.3`. That is a real
operational cost — sid is a moving target — and it is recorded here rather than
hidden.

---

## Freshness and latency

| Datum | Freshness | Source |
|---|---|---|
| Session byte counters | **Only as fresh as Interim-Update or Stop.** `Acct-Interim-Interval` has a widely-implemented **300-second floor**, and many NAS send no interim updates at all unless the server requests them. | DOCUMENTED — RFC 2869 |
| Session byte counters, controller-API path | **May not exist.** There is no Access-Accept on the UniFi guest path, so Foyer cannot request interim updates; rows appear only if the operator separately attached an accounting profile. | DOCUMENTED — see [`nas-drivers.md`](rules/nas-drivers.md) |
| "Currently connected clients" | Foyer knows only what accounting told it. A NAS that reboots without `Accounting-Off` leaves sessions open **forever** until the reaper closes them. | DOCUMENTED |
| Client hostname / OS | **Not available.** Would require DHCP fingerprinting, which is out of scope ([`architecture.md`](architecture.md) §13 — that is NAC territory). **The contract agrees** — see below. | DOCUMENTED |
| Client MAC as a stable identity | **Not stable.** iOS 14+ and Android 10+ present a rotating per-SSID private MAC by default. | DOCUMENTED |

**UI consequence:** there is no such thing as a real-time per-client bandwidth
graph in Foyer. Quota is *sampled*, and the interval must be shown next to the
number.

**There is no `Session.hostname` in the contract, the fixtures or the mock.** The
row above says the datum does not exist. A field documented as "usually null", with
fixtures populating it for a handful of rows, would make the console show `iPhone`,
`iPad`, `MacBook-Air` — and break the rule that a screen may show only what has been
shown to exist ([`docs/rules/frontend.md`](rules/frontend.md)).

Anyone who sees one device name concludes Foyer can show device names, and every
screen designed afterwards assumes a column that will be empty on real hardware. Removing it from the *contract* rather
than from the render is what disarms that, because a field in the schema is an
invitation to the backend as much as to the UI.

**What would reopen it is a measured producer, not a request.** DHCP fingerprinting
stays out of scope ([`architecture.md`](architecture.md) §13 — that is NAC territory), so the evidence would
have to be a `radacct` column a real NAS actually populates, recorded here (see
*Baseline facts*). Nothing short of that.

### Byte counters are 32-bit, and they wrap

Sampling is not the only reason a byte figure lies. The counters themselves are
not monotonic.

| Fact | Status |
|---|---|
| `Acct-Input-Octets` and `Acct-Output-Octets` are **32-bit**. They wrap at 4 GiB. | DOCUMENTED — RFC 2866 §5.3–5.4 |
| `Acct-Input-Gigawords` and `Acct-Output-Gigawords` exist for exactly one reason: to carry the high-order 32 bits the octet counters cannot hold. | DOCUMENTED — RFC 2869 §5.1–5.2 |
| A server that ignores Gigawords therefore reports a session's total **dropping by 4 294 967 296** mid-session. The session appears to run backwards every 4 GiB. | DOCUMENTED — the two above, together |
| **Whether a NAS sends Gigawords at all cannot be established from `radacct`.** The stock schema stores `acctinputgigawords`/`acctoutputgigawords` as an integer defaulting to zero, so "sent as zero" and "never sent" are **the same row**. There is no query that separates them. | DOCUMENTED — stock FreeRADIUS `radacct` schema |

**UI consequence:** **no figure obtained by subtracting two counters may be
rendered without handling the wrap.** "Used" computed as a difference goes
negative; "remaining" computed as allowance-minus-used jumps back up and hands
the guest 4 GiB of free quota; a delta-based rate estimate reads as a wildly
negative throughput. This is a *display* rule as much as an arithmetic one — a
negative duration or a shrinking total must be shown as the anomaly it is, never
clamped to zero and never quietly hidden.

The fixture session `sess-counter-wrap` wraps roughly once per mock cycle so that
the first thing to try it fails immediately and visibly.

**The contract carries the tripwire the rule above needs.** Without it no field would
say which of the two a smaller number is, and a counter that had wrapped would render
as a plain fact on a healthy access point.

| Field | What it is |
|---|---|
| `Session.countersWrapped` | Foyer has seen this session's reported octet counter go **down** between two accounting records with no restart in between. **Required, not optional** — an absent boolean arrives as `undefined` and a component reads that as "no", which is the bug the field exists to remove. **Sticky** for the life of the session: once a counter has wrapped, every figure derived from it stays a floor, including figures taken after the next clean interim update. |
| `Session.inputOctetsTotal` / `outputOctetsTotal` | The reported counter plus 4 294 967 296 per observed wrap. Equal to the reported value while the flag is false, `null` wherever the reported value is null. |

Two rules follow, and both are deliberate:

- **The reported octet counters are never repaired in place.** `inputOctets` and
  `outputOctets` are exactly what the NAS last said. The reported value is the
  **evidence**; the total is an **interpretation**, and silently overwriting the
  first with the second makes the anomaly unprovable from the API.
- **The totals are a lower bound, not a total, once the flag is set.** Two wraps
  between two accounting records are indistinguishable from one, and at the
  300-second interim floor 4 GiB in one interval is **114 Mbps** — reachable on the
  gigabit uplink a hotel actually has. Render it as "at least", or render the
  reported counter with the anomaly beside it. Never as a measurement.

**There is deliberately no "does this NAS send Gigawords" field.** The row above is
why: sent-as-zero and never-sent are the same `radacct` row, so such a field could
only ever carry a guess. `countersWrapped` is keyed on an **observed decrease** — two
consecutive reads and nothing else — rather than on a claim about the device. The
arithmetic sits on the server side of the wire on purpose: a wrap rule that every
client re-implements is a wrap rule one client gets wrong.

---

## Vendor behaviour

| Vendor | Captive-portal authorization | Redirect attributes | Notes |
|---|---|---|---|
| UniFi | Controller API (`cmd/stamgr` → `authorize-guest`). **No RADIUS equivalent.** | n/a — the controller redirects | Ubiquiti documents RADIUS CoA on port 3799, so *revocation* can use CoA even though authorization cannot. DOCUMENTED. |
| Cisco | RFC 5176 CoA | `url-redirect`, `url-redirect-acl` — **Cisco AV-pairs, not a standard** | CoA port frequently **1700**, not 3799. `url-redirect-acl` names an ACL that must already exist on the NAS; Foyer cannot create it. DOCUMENTED. |
| Aruba | RFC 5176 CoA | `Aruba-User-Role`, `Aruba-Captive-Portal-URL` (AOS 8.4+) | DOCUMENTED |
| Ruckus | **Northbound Interface JSON API** | `Ruckus-External-Url` | Not a CoA vendor for captive portal. PacketFence documents that RADIUS deauth *fails* here in non-proxy MAC-auth mode unless the deauthentication method is set to HTTP/HTTPS. DOCUMENTED. |
| MikroTik | RFC 5176 CoA | — | Hotspot requires an external RADIUS server. DOCUMENTED. |

### UniFi redirect parameters

`ap` (AP MAC), `id` (client MAC), `t` (timestamp), `url` (original URL), `ssid`.

**These are unsigned query parameters.** Newer controller versions add an encoded
`ec` variant, not universally. **Never create a grant from them without
`NASDriver.VerifyPresence()`.** DOCUMENTED — see [`docs/rules/security.md`](rules/security.md) §4.

Confirmed verbatim against Ubiquiti's own worked example in
[External Hotspot API for Authorization Clients](https://help.ui.com/hc/en-us/articles/31228198640023-External-Hotspot-API-for-Authorization-Clients):
`?ap=94:2a:6f:d0:30:57&id=1c:71:25:63:e4:24&t=1742398732&url=…&ssid=dph-guest`.
**The access point is identified by MAC and by nothing else.**

**MEASURED** with an iPhone joining an open UniFi guest SSID served by a UniFi access
point, captured device-side. The probe to `captive.apple.com` is
answered `302` to the portal:

```
Location: http://192.0.2.10:80/guest/s/default/
          ?ap=00:00:5e:00:53:01&id=00:00:5e:00:53:a1&t=1700000000
          &url=http://captive.apple.com%2Fhotspot-detect.html&ssid=Guest
```

| Question | Answer | Standing |
|---|---|---|
| Does the AP redirect to `/guest/s/default/`? | **Yes**, on port 80, as an absolute URL, before the portal has ever been visited | MEASURED |
| Are the parameters `ap,id,t,url,ssid`? | **Yes**, in that order, all five, every time | MEASURED |
| **Is `ap` the BSSID or the AP's device MAC?** | **The AP's device MAC** — not the BSSID the phone associated to (the 5 GHz radio's BSSID differs from the device MAC, and cannot be computed from it). Foyer resolves `ap` against the controller's **device** list; resolving it as a BSSID would make every presence check miss a real client | MEASURED |
| What is `id`? | The client MAC as the AP sees it — on iOS a locally-administered Private Wi-Fi Address, different from the address the same phone uses on other networks | MEASURED |
| What is `t`? | **Unix epoch seconds, generated at the moment of the redirect.** Two redirects one second apart differ by one | MEASURED |
| Is `url` percent-encoded? | Partly — `/` is, but a `&` inside it is not escaped, so **a raw `url` is not safe to concatenate without re-encoding** | MEASURED |
| Does the AP retry the probe? | Repeatedly, on a fresh source port each time, while the portal is open; every retry re-generates `t` | MEASURED |

### Omada redirect parameters — different, and different again when wired

From
[TP-Link/Omada KB 70050](https://www.tp-link.com/us/support/faq/3231/) (Controller
5.0.15+, page dated April 2026). DOCUMENTED.

| Path | Parameters |
|---|---|
| EAP (wireless) | `clientMac`, `apMac`, `ssidName`, `radioId` (0 = 2.4 G, 1 = 5 G), `site`, `t` (**microseconds**), `redirectUrl` |
| Gateway (wired) | `clientMac`, `gatewayMac`, `vid`, `site`, `t`, `redirectUrl` — **no `apMac`, no `ssidName`, no `radioId`** |

**UI consequence:** a portal that assumes one parameter set cannot serve a wired
guest, and a grant keyed on "the AP the guest is under" has no AP on the wired path.
What remains open on real Omada equipment is whether devices send anything *beyond*
what is documented.

### The controller is the object, and the RADIUS client set is not the same set

From vendor documentation and shipped integrations; the resulting model is the
*Object model* section of [`architecture.md`](architecture.md). Several separate facts
that a single "NAS" object would fuse into one:

| Fact | Status |
|---|---|
| UniFi guest authorization is entirely controller- and site-scoped: `GET /v1/sites` → `GET /v1/sites/{siteId}/clients?filter=macAddress.eq(…)` → `POST /v1/sites/{siteId}/clients/{clientId}/actions` with `{action: "AUTHORIZE_GUEST_ACCESS", timeLimitMinutes, dataUsageLimitMBytes, rxRateLimitKbps, txRateLimitKbps}`. The access point is never an addressable object. | DOCUMENTED — Ubiquiti Help Center, article 31228198640023 |
| That API requires **Network Application ≥ 9.1.105** (March 2025), with the key generated in Network → Control Plane → Integrations, sent as `X-API-KEY`. Older controllers need the legacy private API and a **local admin username and password**. | DOCUMENTED — same |
| The client-lookup call returns `access: { type: "GUEST", authorized: false }`, which is `VerifyPresence` **and** the still-unauthorized check [`security.md`](rules/security.md) §4 requires, in one call. | DOCUMENTED — same |
| Access points are **discovered from the controller**, not configured: PacketFence's `populateAccessPointMACIP()` walks `GET /api/s/{site}/stat/device/` and caches `mac → ip` for every AP *and every BSSID in its `vap_table`*, because the AP MAC in the redirect is otherwise unresolvable. | DOCUMENTED — [`pf::Switch::Ubiquiti::Unifi`](https://github.com/inverse-inc/packetfence/blob/devel/lib/pf/Switch/Ubiquiti/Unifi.pm) |
| **Every access point is its own RADIUS client, but they share one shared secret.** The secret is a property of a server entry inside a controller-level RADIUS profile; the client address is each AP's own. So `clients.conf` needs N addresses (or a CIDR) against **one** secret. | DOCUMENTED — [Portnox admin guide](https://docs.portnox.com/topics/nas_aps_employee_ubiquiti) for the profile shape; practitioner reports for the address ([dannyda.com 2021-05-21](https://dannyda.com/2021/05/21/what-is-the-client-ip-address-for-freeradius-windows-radius-server-when-configuring-ubiquiti-unifi-controller-with-external-radius-server-non-unifi-radius-server/)) |
| The pattern is not UniFi-specific. HPE Aruba documents `dynamic-radius-proxy` as *"an alternative to adding all APs as NAS clients"* — the feature exists because the default is one client per AP, and it is **an operator toggle**, so identical hardware yields one RADIUS client or twelve depending on a setting Foyer cannot see. | DOCUMENTED — [arubanetworking.hpe.com CLI bank](https://arubanetworking.hpe.com/techdocs/CLI-Bank/Content/instant/dyna-rad-prox.htm) |
| UniFi OS silently answers **403** to every `cmd/stamgr` POST if the `X-CSRF-Token` from the login response is not echoed. Omada requires the same thing under the header name `Csrf-Token`, from `result.token` in its hotspot-login response. | DOCUMENTED — [PacketFence #9107](https://github.com/inverse-inc/packetfence/issues/9107); TP-Link KB 70050 |
| Controller flavour must be **probed, not asked**: `GET /proxy/network/status` answering 200 or 401 means UniFi OS (login `/api/auth/login`, prefix `/proxy/network`, ports 80/443); anything else means a standalone controller on **8443** with `/api/login` and no prefix. | DOCUMENTED — PacketFence `_connect()` |
| Controllers ship **self-signed certificates**, and every shipped integration disables verification — PacketFence sets `verify_hostname => 0`; TP-Link's own PHP sample sets `CURLOPT_SSL_VERIFYPEER = FALSE`. A registration form that offers no TLS choice will fail on first contact for most deployments. | DOCUMENTED — both sources |
| On UniFi OS the Integration API lives under `/proxy/network/integration/v1` and accepts `X-API-KEY` (any case); `Authorization: Bearer` is refused `401`. The legacy login (`POST /api/auth/login` with a local admin) answers with an `X-CSRF-Token` that must be echoed on every write, or POSTs answer `403`. | MEASURED |
| The legacy device list (`/proxy/network/api/s/{site}/stat/device`) carries each AP's device `mac` and a `vap_table` whose `bssid` values differ from it. An Integration-API client's `uplinkDeviceId` resolves, through `GET /integration/v1/sites/{id}/devices`, to the access point it is attached to. | MEASURED |
| A second `AUTHORIZE_GUEST_ACCESS` with different limits **replaces a live authorization without disconnecting the client** (`connectedAt` unchanged). `Capabilities.LiveReauthorize` is true for UniFi. | MEASURED |

**Omada needs two fields Foyer does not ask for and cannot derive:** the
**`CONTROLLER_ID`**, a 32-character identifier that appears only in the controller's
own URL, and a **hotspot operator** account created in Hotspot Manager — TP-Link is
explicit that it is *"rather than the account and password for the controller
account"*. DOCUMENTED — TP-Link KB 70050.

**The Omada external-portal API has no documented presence check**, and its
authorize call (`{clientMac, apMac, ssidName, radioId, site, time, authType}`)
carries **no data limit and no rate limit**. So on that path `hardQuota` and
`bandwidthLimits` are false, and `VerifyPresence` — which [`security.md`](rules/security.md) §4 makes
mandatory — has no implementation. Both are **DOCUMENTED by absence** from the
vendor's own required-parameter list, which is weaker than a measurement; the likely
resolution is the separate Omada Open API (`/openapi/v1/…`, client id + secret), and
that is **UNVERIFIED**.

### Not every session figure dies with accounting

| Fact | Status |
|---|---|
| On the UniFi controller path, `GET /v1/sites/{siteId}/clients/{clientId}` returns `access.authorization.{authorizedAt, expiresAt, dataUsageLimitMBytes}` — so **who is authorized, since when, until when, and under what limit** are all available with no `radacct` row in existence. What is unavailable is bytes used. | DOCUMENTED — Ubiquiti Help Center, article 31228198640023 |
| The controller path **does** carry usage — **on the detail endpoint only**: `access.authorization.usage` with `rxBytes`, `txBytes`, `bytes`, `durationSec`. A list of clients and the detail view of one client are therefore not obliged to agree about bytes, and a UI must not render them as the same figure. | MEASURED |

**Contract consequence:** when `Nas.accountingConfigured` is `missing`, every
accounting-derived byte counter for that NAS is absent — but **the authorization
window is not**. Read claims like this narrowly: *UniFi cannot do CoA* means its
captive-portal *authorization* has no RADIUS equivalent (its revocation does);
*`rotateSecret`* gates the *push*, not generating a secret; and *no external resource*
means no external **origin** (below).

---

## Captive Portal API (RFC 8908 / 8910)

| Claim | Status |
|---|---|
| API endpoint **MUST** use an `https` URI; client **MUST NOT proceed** if it cannot validate the certificate (DNS-ID matching the provisioned hostname) | DOCUMENTED — RFC 8908 §4/§4.1 |
| Response `Content-Type` **MUST** be `application/captive+json` | DOCUMENTED — RFC 8908 |
| Only `captive` is a required member; `user-portal-url`, `venue-info-url`, `seconds-remaining`, `bytes-remaining`, `can-extend-session` are optional | DOCUMENTED — RFC 8908 |
| iOS 14+ and macOS Big Sur+ support it | DOCUMENTED — Apple, IETF captive-portals list, June 2020 |
| Android 11+ supports **a subset** | DOCUMENTED — Google, `android.net.CaptivePortalData`, API 30 |
| Windows supports it | **NO — confirmed negative.** Microsoft's own current NCSI documentation (updated February 2026) describes captive handling end to end with no mention of RFC 8908/8910 or option 114. Do not claim it. |

### The exact subset each client parses

From reading AOSP and ChromiumOS source directly.

| Client | Reality |
|---|---|
| **Android** | Parses `captive`, `user-portal-url`, `venue-info-url`, `bytes-remaining`, `seconds-remaining`. **`can-extend-session` is not parsed at all** — six years after the release notes described it. `captive` is **mandatory and must be a boolean**; if it is missing or stringy the *entire payload is silently discarded*. Hard **4096-byte** response cap (`CAPPORT_API_MAX_JSON_LENGTH`). **DHCPv4 option 114 only** — IPv6 RA option 37 carries a literal `// TODO` in `IpClient.java`, and DHCPv6 option 103 is absent. An invalid certificate causes a **silent** fallback to legacy probing with no diagnostic. |
| **iOS / macOS** | Implemented since iOS 14. **No UI is rendered for `seconds-remaining` or `venue-info-url`.** A first-join defect (iOS ignores option 114 on first association and only uses the API on reconnect) was measured on iOS 14–16; Apple closed the bug, reports continued. Unmeasured on later releases. |
| **ChromeOS** | The **most complete implementation found** and usually omitted from support lists — ChromiumOS `shill` handles all six keys, enforces HTTPS, and supports both DHCP 114 and the RA path. A malformed `venue-info-url` fails the *entire* parse. |
| **Linux NetworkManager** | Not implemented; the merge request was **closed unmerged in March 2025**. `nm-dhcp-options.c` marks option 114 `include = FALSE`. |
| **systemd-networkd** | RFC **8910** since v254 (records the URL, shows it in `networkctl status`). RFC **8908 is not implemented** — it never fetches the JSON. |

**Errata that will bite an implementer:** RFC 8910's text names the media type
`application/capport+json`. That is **wrong** — errata **EID 6620** (verified
June 2021) corrects it to `application/captive+json`. Anyone implementing from
RFC 8910 alone gets it wrong.

**The CAPPORT working group is concluded.** Nothing formally updates or extends
RFC 8908. The only post-2020 draft (`client-authentication-url`, Apple, 2024)
expired without WG adoption, though it did reach the IANA registry.

### One real deployment, measured

GovWare 2025, ~1,700 attendees, portal built on RFC 8908/8910
(Cisco Security Business Group, published December 2025):

- iPhone — *"worked flawlessly."*
- Android, Samsung, Linux — *"requested option 114 but did not honor going to the captive portal"*; DNS sinkholing was required anyway.
- Windows — *"did not ask for the option at all."*
- Older macOS — still required `/hotspot-detect.html` returning `SUCCESS`.

**Consequence:** a LAN appliance with no public DNS name cannot serve RFC 8908 at
all. Interception is the default path and is **not optional**; RFC 8908 is an
accelerator for Apple devices and a no-op on Windows and Linux desktops. Both
paths ship and both are tested.

**UI consequence:** build **no UI** on `seconds-remaining`, `bytes-remaining`,
`can-extend-session` or `venue-info-url`. Apple surfaces none of them and Android
does not even parse the third. This is the same failure mode as the `radacct`
sampling rule — the protocol defines a field, clients ignore it, and a UI built on
it lies.

---

## Client environment

Sourced from Apple's shipped binaries, AOSP source, and vendor engineering
reports. **This section is what the guest portal is designed
against.**

### The dismiss mechanism — the single hardest constraint

| Fact | Status |
|---|---|
| The iOS/macOS CNA re-checks captivity **only after a full URL navigation, or on a timer**. A `fetch`/XHR call does **not** trigger a re-check. | DOCUMENTED — Wireless Broadband Alliance, `captivebehavior.wballiance.com` |
| If no full navigation occurs, the CNA's button stays **"Cancel"** — and Cancel **disassociates the device from the SSID**. The user is authenticated and then thrown off the network. | DOCUMENTED — same source |
| While the portal is open, Wi-Fi is active but **is not the default route**. On failure "the device leaves the network" — a designed outcome, not a bug. | DOCUMENTED — Apple DTS (Quinn), developer forums thread 725593, 2023-02 |
| Android calls `reevaluateNetwork()` from `onPageStarted`/`onPageFinished` only — the same navigation-driven model. | DOCUMENTED — AOSP `CaptivePortalLoginActivity.java` |
| Android **destroys the login WebView** the moment the network validates (`NET_CAPABILITY_VALIDATED` → `done(DISMISSED)` → `finishAndRemoveTask()`). | DOCUMENTED — AOSP, same file |

**UI consequence, frozen:** the portal's success path is a real HTTP navigation
(form POST → 303 → new document). A single-page app that submits by `fetch` and
re-renders is a total failure that Playwright reports as a pass. And nothing the
user must see may be placed *after* authorization succeeds — Android closes the
window. Terms, session detail and venue links come **before** the grant call.

### What the CNA actually is, and what it can do

Measured by static analysis of the shipped macOS `Captive Network Assistant.app` (v5.0). **iOS ships a separate implementation that could not be inspected — do
not assume parity.**

| Capability | Finding | Status |
|---|---|---|
| Engine | `WKWebView` on system WebKit — imports `WKWebViewConfiguration`, `WKProcessPool`, `WKWebsiteDataStore`. Not a reduced JS interpreter. | MEASURED |
| Storage | `initNonPersistentConfiguration` — an **ephemeral** `WKWebsiteDataStore`. Cookies, `localStorage`, `sessionStorage`, IndexedDB and service-worker registrations are memory-only and destroyed on close. | MEASURED |
| JavaScript | **Fully enabled.** The CNA itself injects JS (`_autoFillUserCredentialsUsingJavaScript`, `_scrapeUserCredentialsUsingJavaScript`) via `callAsyncJavaScript:`. It reads the login form's input elements. | MEASURED |
| New windows | `window.open`/`target=_blank` are *handled* but there are no tabs and Safari cannot be launched; the navigation replaces the current view. Design as if popups do not exist. | MEASURED (static) + DOCUMENTED (community, 2016–2024) |
| App switching | Switching to another app (SMS, authenticator) **destroys the CNA session and disconnects the device**. | DOCUMENTED — WBA |
| Cross-origin redirects | Followed. What fails is *coming back with state*, because the data store is ephemeral. **This — not a redirect block — is why OAuth/social login cannot work in the CNA.** | MEASURED |
| Plain HTTP | Allowed — `NSAllowsArbitraryLoadsInWebContent = 1`. | MEASURED |
| macOS window | **900 × ~550 pt**, from `CNAWindowController.nib`. No equivalent figure exists for iOS. | MEASURED |
| `prefers-color-scheme`, `env(safe-area-inset-*)`, `@font-face`, modern CSS | **NOT FOUND** for iOS. No primary or measured source exists. Inference from "it is system WebKit" is reasonable but **unproven** — see Open unknowns. | UNVERIFIED |

"The CNA has limited JavaScript" is wrong as stated. The engine is complete; the
*environment* is stripped. Designing around a crippled interpreter solves the wrong
problem.

**UI consequence:** 550 pt is the *whole* window and it does not grow — the CNA is
not a browser viewport with a resizable frame around it, and minus its chrome the
usable box is **900 × 512**. At full vertical rhythm the welcome document is about
630 px tall, which puts the venue's terms and the consent sentence below the fold in
the one environment where the guest cannot make the window bigger. Consent text a
guest has to go looking for is consent text they do not read.

Hand-picking margins on one page against one sample of content does not hold: it
misses the other documents and the next venue's terms. **The portal applies one
multiplier, `--v`, to every vertical gap in the stylesheet, in two steps.** Nothing
else keys on viewport height, so it applies to pages not written yet and cannot be
tuned against a sample. Measured on the running product, in both languages:

| Step | Value | What it is for, and what was measured |
|---|---|---|
| `max-height: 720px` | `0.8` | 360 × 640 and 375 × 667 budget Androids. Both put the consent line below the fold at full rhythm — **28 px in English, 46 px in Vietnamese** — and neither needs the CNA's compression to recover it. |
| `max-height: 620px` | `0.6` | The CNA, and landscape. At 0.6 the welcome page **and** the success page both fit **900 × 512** in English *and* Vietnamese, with the fixture venue's real terms, on the plan that renders the most rows. |

Tall phones — 390 × 844, 412 × 915, 360 × 780 — stay at `1` and are untouched.
**Vietnamese is the binding measurement**: its longer copy overflows a rhythm tuned in
English. Type size, colour and touch-target size are **not** in the multiplier — the
minimum heights on the inputs, the button and the method rows hold at any `--v`, so
compression comes out of space and never out of legibility or reach. Every paragraph
sets both of its margins, because the user agent's default `1em` on an unset one adds
up quickly at this height.

**A rhythm is not a guarantee, so there is a second mechanism.** `terms.summary` is
free text a venue writes: a venue can always exceed any window, and a 740 × 360
landscape phone cannot fit the welcome page at any rhythm worth having. At the 620 px
step the portal styles `::-webkit-scrollbar`, which opts the document out of macOS
overlay scrollbars and out of the Android WebView's fading one, so when content
genuinely does not fit there is a **persistently visible bar saying so** rather than
a page that looks complete and is not. `scrollbar-color` rides along for engines that
have it; the CSS floor (Chromium 117) predates it, so it cannot be the only mechanism.

**These are browser-window measurements at the CNA's numbers, not measurements
inside a CNA**, and **no equivalent figure exists for iOS**. 900 × 550 is a floor
that happens to be measurable, not the worst case. An on-device viewport measurement
of the iOS CNA is the one that would move this most (see *Open unknowns*).

### Android login WebView — the exact settings

Read from AOSP `packages/modules/CaptivePortalLogin`, `android17-release`.
The login app ships as a Play system module, so **`Build.VERSION` does not predict
its behaviour** — a 2019 handset can be running a 2026 login app.

| Setting | Value | Consequence |
|---|---|---|
| `setJavaScriptEnabled` | `true` | — |
| `setDomStorageEnabled` | `true` | `localStorage` works — unlike the CNA |
| `CookieManager` | persistent, third-party accepted | cookies survive, unlike the CNA |
| `clearCache(true)` | called on **every launch** | **there is no warm second load. Every byte is paid every time.** |
| `setSupportMultipleWindows` | not called → `false` | `target=_blank` replaces the page, no back affordance |
| `setUseWideViewPort` + `setLoadWithOverviewMode` | `true` | omitting `<meta name="viewport">` yields a ~980 px viewport scaled to fit |
| `setForceDark` / `setAlgorithmicDarkeningAllowed` | never called | **no auto-darkening — ship a real dark stylesheet** |
| Safe-area insets | applied only on Android 15+, and **only the top** | a bottom-pinned CTA can sit under the gesture bar |
| Non-http schemes | `tel:` and `sms:` work; `mailto:`, `intent://`, `market://` are **hard-blocked behind a scary interstitial** | no mailto support link on the portal |
| Page-load timeout | none — `SOCKET_TIMEOUT_MS` is declared and never referenced | a slow portal shows a progress bar forever rather than failing |

### Input methods — the voucher field is typed through one

The single most-used control in Foyer takes hand-typed text on a phone, often
through an IME (Vietnamese Telex and VNI input are the common case for a shipped
language).

| Fact | Status |
|---|---|
| An IME composes *inside* the field and reads the value back while composing. `InputEvent.isComposing` exists to mark exactly that window. | DOCUMENTED — UI Events, `InputEvent.isComposing` |
| Writing to `input.value` during composition drops or duplicates characters on Gboard and on Vietnamese Telex/VNI input methods. | **UNVERIFIED** on device — the failure mode is well described, the measurement has not been taken |

The portal's voucher-grouping enhancement skips any `input` event whose
`isComposing` is true, unconditionally and on every platform, so **nothing in the
design depends on resolving the second row.** It is recorded because the guard is
invisible and someone will one day be tempted to remove it.

CSS floor for a device still generating portal traffic in 2026 is roughly
**Chromium 117**: grid, flexbox, custom properties, container queries, `:has()`
and `color-mix()` are safe; CSS nesting, `@scope` and view transitions are not.
Devices with no Play Store (Huawei post-2019, grey-market handsets) never update
WebView and are the real floor risk. Status: DOCUMENTED for the update mechanism,
UNVERIFIED for the specific per-OS ceiling.

### Android MAC randomisation is friendlier than assumed

| Fact | Status |
|---|---|
| Android 10+ defaults to **persistent** per-SSID randomisation — the address "remains the same until a factory reset". Android 12's *non-persistent* mode is gated on "an open network that **hasn't encountered a captive portal**". | DOCUMENTED — source.android.com |

So on an SSID that presents a portal, Android keeps a **stable per-SSID
pseudonym**. "Remember this device" is viable on Android. It is still a
locally-administered pseudonym, so this **supports** [`security.md`](rules/security.md) §6 rather than
relaxing it — but the UI may show a returning-device hint rather than treating
every visit as new.

### Measured byte budget for the portal

Measured on production builds.

| Artefact | gzip | brotli |
|---|---|---|
| React 19 + `react-dom/client` runtime alone | **68.3 kB** | 56.9 kB |
| Vite build, one voucher form, React 19 | **60.7 kB** | 51.1 kB |
| Vite build, same form, Preact 10 | **5.9 kB** | 5.3 kB |
| No-framework single-file portal page, correct metas, real `<form>` | **0.86 kB** | 0.62 kB |
| Be Vietnam Pro, 2 weights, latin + vietnamese subsets | **36.2 kB** | — |
| Inter variable, latin + vietnamese subsets | **58.7 kB** | — |
| **Foyer portal as built** — largest single document, HTML with CSS and JS inlined, system font stack | **4.2 kB** | — |

The Vietnamese unicode-range (`U+1EA0-1EF9`, `U+20AB` ₫, and the combining marks)
costs **+4.7 kB on Be Vietnam Pro, +10.3 kB on Inter**. Vietnamese support is not
the expensive part; the base Latin face is.

**UI consequence:** React 19 spends the entire realistic portal JS budget on its
runtime before a line of portal code is written, on a link that is worse than any
published performance baseline, with a cache that Android guarantees is cold. The
portal budget is **≤ 50 kB total with a system font stack, ≤ 90 kB with a
webfont**. That is why the portal is Preact 10 with CSS and JS inlined
([`docs/rules/frontend.md`](rules/frontend.md)).

The last row is what that decision produced: the built portal's heaviest document
is one HTTP request at **8% of the budget**. Because Android calls
`clearCache(true)` on every launch and the CNA has no persistent store, a linked
stylesheet or script is refetched every time and a content hash buys nothing — so
inlining costs one page-weight and saves two round trips on the worst link of the
guest's day.

### A venue photograph cannot be inlined, and the reason generalises

Measured with one real photograph, downscaled to portal
widths, JPEG quality 70, then base64-encoded and gzipped as it would be inside a
document.

| Width | JPEG | gzip | as a data URI | **data URI, gzipped** |
|---|---|---|---|---|
| 900 px — macOS CNA at 1× | 89,697 B | 89,058 B | 119,619 B | **89,741 B** |
| 1170 px — a phone at 3× | 150,652 B | 149,467 B | 200,895 B | **150,545 B** |
| 1800 px — CNA at 2× | 313,189 B | 309,919 B | 417,611 B | **312,270 B** |

Against a **51,200 B** per-document budget, the smallest useful size is **1.75×
the whole budget on its own**. Note the last column against the second: base64
inflates by a third and gzip gives back exactly that and no more, so the data URI
lands *fractionally larger than the raw file*. **Inlining a photograph costs the
full file size and buys nothing.**

**The generalisation.** *"Every external resource would also have to be added to the
walled garden on every driver"* is true of **external** resources. The portal's own FQDN is already required to be inside the
garden — [`security.md`](rules/security.md) §5, or the portal cannot serve itself — so a **same-origin**
asset is reachable by construction. And the inlining rule for CSS and JS rests on a
different fact (Android's cold cache), which argues for one request per document at
3 kB and argues the other way at 90 kB: linked, the document renders at 4 kB while
the image arrives after; inlined, the guest waits for 94 kB before seeing a word.

So the rule is **no external origin**, not **no linked asset**.

**Gate consequence:** those are two different rules with two different checks.
`web/tools/check-budget.mjs` refuses an absolute URL to another origin, and it also
follows every same-origin subresource a document references and weighs it — a gate
that only grepped for `https?://` would let a same-origin `<img>` of any size through
unweighed.

### The venue photograph, measured against every target viewport

Measured on the running portal with a real 89,697 B photograph uploaded
through the admin API, welcome page, **both languages**.

| Viewport | `--v` | Hero | Document | Result |
|---|---|---|---|---|
| 900 × 512 — the macOS CNA box | 0.6 | hidden | 512 | fits exactly |
| 900 × 512 — *hero forced visible* | 0.6 | 72 px | **607** | **95 px over**, consent line 76 px below the fold |
| 375 × 667 | 0.8 | hidden | 667 | fits exactly |
| 375 × 667 — *hero forced visible* | 0.8 | 96 px | **696** | **29 px over** |
| 360 × 640 | 0.8 | hidden | 640 | fits |
| 390 × 844 — iPhone 14/15 | 1 | 120 px | 844 | fits |
| 412 × 915 — Pixel | 1 | 120 px | 915 | fits |
| 740 × 360 — landscape | 0.6 | hidden | 509 | 149 px over — **pre-existing**, and why the persistent scrollbar exists |

Two rules come out of this and both are in `portal.css` with the numbers attached:

- **The photograph is hidden below 720 px of viewport height.** At 375 × 667 and at
  900 × 512 the welcome page already fills the window *exactly*, so there is no hero
  height that fits — this is a hide rather than a smaller number because no smaller
  number exists. The priority is explicit: **the consent sentence is content and the
  photograph is decoration.**
- **The height is 120 px, not 140.** At 140 px the page overflowed 390 × 844 by
  **6 px in Vietnamese and not at all in English.** Vietnamese is the binding
  measurement every time: a rule tuned in English is not a rule until Vietnamese has
  been measured against it.

### Click-through: what a required control costs, per configuration

Measured on the running portal, welcome page, both languages. The consent
button is a **required control** — it cannot be hidden the way the photograph can,
and it cannot shrink, because a 44 px touch target is the one thing `--v` never
compresses.

| Methods offered | 900 × 512 (CNA) | 375 × 667 | 360 × 640 | 390 × 844 |
|---|---|---|---|---|
| voucher + account + click-through | **570 — 58 over** | 690 vi — **23 over** | 690 vi — **50 over** | 844 fits |
| click-through alone | 512 fits | 667 fits | 640 fits | 844 fits |

The page already filled the first three viewports *exactly*, so any required addition
overflows them and no rhythm fixes it. Two consequences, both shipped:

- **The persistent scrollbar starts at 720 px, not 620.** It existed for the CNA step
  because venue terms are free text; click-through made the same thing true one step
  higher. A page that looks complete and is not is the failure — an overlay scrollbar
  produces exactly that.
- **The admin states the cost where the choice is made.** An operator cannot measure
  the CNA window and will not otherwise find out. Offering all three is legal and
  says so; terms-only on its own is the usual choice and fits everywhere.

740 × 360 landscape is over in every configuration and was before any of this — see
the note on the `--v` steps.

### Playwright still cannot prove any of this

| Constraint | Status |
|---|---|
| Playwright cannot emulate the CNA — portal E2E goes green while real phones fail | DOCUMENTED — hence the device matrix in tier 4 |
| The Cancel-instead-of-Done failure is **invisible to Playwright** and fatal on hardware | DOCUMENTED |

---

## FreeRADIUS

All rows below were taken on **3.2.10** in `lab/` unless stated otherwise.

### `radiusd -XC` is insufficient, demonstrated

The man page says so; this is the demonstration. **A candidate started with `-n`
passed gate 1 and exited immediately at gate 2. A candidate pointed at an unreachable
policy engine passed gates 1 *and* 2, and failed only at gate 3.** MEASURED.

Two smaller facts: `radiusd -n <name>` reads
`<name>.conf` instead of `radiusd.conf` — **it does not name an instance**; and
`radiusd -d <dir>` really does redirect `${modconfdir}`, proven with a deliberately
broken `queries.conf`. `-XC` also performs the SQL client load and fails when
PostgreSQL is unreachable, which is more than expected of it.

### `require_message_authenticator` cannot be a property of a SQL client

`rlm_sql` reads exactly `id, nasname, shortname, type, secret, server` for a client.
**There is no seventh column and `client_query` cannot carry one**, so the BlastRADIUS
mitigation in [`docs/rules/security.md`](rules/security.md) §3 cannot be emitted per client once clients
come from SQL — which [`radius.md`](rules/radius.md) requires them to.

The guarantee is not lost; it moves, and it ends up **stronger**: `security {}`
globally, plus each network section, plus the dynamic-clients site. A global `yes`
cannot be weakened by a row in a table. But `nas.require_message_authenticator` and
`nas.limit_proxy_state` exist in the schema and are **inert** — a column that looks
like a switch and is not.

MEASURED. The three places are each pinned by a snapshot test.

### Two different failures are identical on the wire and distinguishable only in the log

Measured on a deployment taken from zero sites to an answered packet.

| Cause | On the wire | In `radiusd`'s log |
|---|---|---|
| The client is not in the table | **silence** | `Ignoring request … from unknown client 127.0.0.1` |
| The client is in the table, **wrong shared secret** | **silence** | `invalid Message-Authenticator! (Shared secret is incorrect.) (from client …)` — **names the client** |

**A guest, an access point and `radclient` see the same nothing in both cases.** The
operator's two remedies — *run the projection sync* and *check the shared secret* — are
opposite actions, and nothing on the network tells them apart.

**The tell is that a shared-secret failure names a client and an unknown-client failure
names an address.** That is `foyerctl doctor`'s job, and the same *"names a client"* signature separates a
wrong secret from a stopped policy engine.

### The first-run path, end to end

From `select count(*) from sites` = **0** on a freshly migrated database:

1. sign in → 2. `POST /sites` → 3. `POST /nas` (`mikrotik`, no controller) → 4.
`foyerctl sync-nas` → 5. **`radclient` is answered.**

```
Received Access-Reject Id 170 from 127.0.0.1:1812 length 62
	Message-Authenticator = 0x86213be09d7c90bde4f91601e470e9ff
	Reply-Message = "denied by policy"
	Acct-Interim-Interval = 300
```

**An `Access-Reject` is the measurement**, because the identity was never valid — what
changed is that there *is* a reply. `radiusd` logged `Adding client 127.0.0.1/32` and
then reached the policy engine **over the shipped mTLS boundary**, with **no restart**:
the dynamic-client lookup found the row the sync had written. The reply carries
`Acct-Interim-Interval`, which is what [`radius.md`](rules/radius.md) requires so a NAS is asked for interim
updates at all.

### Removing a client is not revoking it, and the two admission paths differ

Measured against FreeRADIUS 3.2.10 with `read_clients = yes`, a NAS on its own bridge
address rather than loopback.

| Fact | Standing |
|---|---|
| A freshly migrated database has an empty client table, and every packet is discarded — *"Ignoring request … from unknown client"* | MEASURED |
| A client written into `nas` by the projection sync is answered, and the server logs `Adding client 172.19.0.4/32` | MEASURED |
| **Immediately after the row is deleted and the sync has run, the running server still answers.** The definition was *discovered*, so the server holds it cached | **MEASURED** |
| After a restart it stops | MEASURED |
| How long it keeps answering | **UNVERIFIED** — `DynamicClientLifetimeSeconds` is 300 *by configuration*; the expiry was not waited out |

**The consequence is a product statement, not an implementation note.** Deleting an
access point **is not a revocation**: the row is gone and the running server keeps
authenticating that client until its cache expires or it restarts. Anything the
console says about removing a NAS has to say that.

**And the two admission paths are not the same client.** A *discovered* client
survives its own deletion; a *startup-loaded* client ignores its own rotation. The
same table, the same row, two behaviours — which is why the change classification is
`none` / `sql-only` / `restart` rather than a boolean.

The rotation half, measured in one minute on one running server:

| | |
|---|---|
| The **new** secret | *"Received packet … with invalid Message-Authenticator! (Shared secret is incorrect.) (from client lab-ap)"* |
| The **old** secret | `Access-Accept` |

**`(from client lab-ap)` is the tell**: a shared-secret failure names a client, and a
policy-engine failure names none. Those two look identical in a support ticket.

### An Access-Request may not carry both `EAP-Message` and `User-Password`

Recorded because it looks like a defect in the server and is not one.

RFC 3579 §3.1 forbids the combination. FreeRADIUS **discards such a packet at the
socket** — *"Bad packet from host: Packet contains EAP-Message and non-EAP
authentication attribute"* — before any virtual server runs, which is why there is no
reply **and no line in the trace even at `-xx`**. A test harness that builds its
request by adding attributes to a template will produce this by accident and read it
as the server ignoring it.

| Packet | Tree | Result |
|---|---|---|
| `EAP-Message` **and** `User-Password` | no `eap` module | discarded, no reply, no trace |
| `EAP-Message` only | no `eap` module | Access-Reject, sub-second |
| `EAP-Message` only | with `eap` module | Access-Reject, sub-second |

MEASURED — three packets across two generated trees. **802.1X in `lab/` is not
affected by it**: `eapol_test` completes EAP-TLS against this generator's output.

### Per-site failure behaviour would render as virtual servers, not as sites

FreeRADIUS knows nothing of a Foyer site, but the sixth column of `client_query` is `server`, which FreeRADIUS maps to a client's
`virtual_server`. So a per-site failure mode can render as **one virtual server per
distinct failure configuration**, with `nas.server` routing each client into it —
**two hundred fail-closed sites would be one server, not two hundred.** The
dynamic-clients path carries the same value as `&FreeRADIUS-Client-Virtual-Server`,
so both admission paths would route identically. DOCUMENTED, and the mapping is measured.

**That routing is planned, not built.** Today the generator renders one failure model
into `default`, and `internal/raddb` refuses at Validate a set of sites that do not
agree on one (`ResolveGuestFailure` in `internal/raddb/config_failure.go`): a
`nas.server` value naming a server the tree does not have makes `radiusd` start,
pass `-XC`, and answer nothing from that NAS, so the routing needs an ordering
invariant between the generator and `nassync` before it can ship.

### The identifier a policy engine may trust is the one the server looked up

`NAS-Identifier` is supplied by the NAS and is therefore attacker-chosen, the same
way UniFi's redirect parameters are ([`security.md`](rules/security.md) §4). `%{client:shortname}` is the
short name of the row FreeRADIUS matched the packet's **source address** against, so
it is the server's own answer rather than the client's claim. The generated `rest`
body carries the latter.

`eap_message` in that body is a **presence marker** (`"present"`), never the blob:
MEASURED, and the EAP octets appear in no request body.

### `Filter-Id` is a name, not a value

From RFC 2865 §5.11. **`Filter-Id` is the *name* of a filter
list that already exists on the NAS.** It carries no bandwidth figure and no policy;
the NAS looks the name up in configuration Foyer did not write and cannot see.

A value such as `Filter-Id = "10000/5000"` is a filter-list name that nothing
implements: a packet carrying it is well-formed, looks like a rate limit, and does
nothing. DOCUMENTED.

**This is the same lesson as `url-redirect-acl`, which also names an ACL that must
already exist on the Cisco NAS.** Two attributes, from two vendors, say the same
thing: *"the operator configures Foyer, not FreeRADIUS" is true; "the operator
configures nothing" is not.* Where an attribute takes a name, Foyer's job is to let
the operator supply one and to say what must exist at the other end — never to invent
a value that looks like a limit.

### A reply attribute cannot be VLAN-tagged on this path

RFC 3580's VLAN assignment is tagged, and FreeRADIUS spells a tag with a colon —
`Tunnel-Type:0`. The Policy API's credential gate refuses colons **by shape**, so a
tagged attribute is not merely dropped: **the whole reply becomes a 503 with a
credential-leak alarm beside it.** Vendor profiles therefore render VLAN attributes
untagged. MEASURED — a profile that emits a tag turns four tests red in
`internal/nas` and one in `internal/aaa`, which is the only place the two gates meet.

### Cisco's captive reply needs two attributes and the transport carries one

`Cisco-AVPair` is repeated: `url-redirect` **and** `url-redirect-acl`. `rlm_rest` is
answered with a JSON dictionary, so a `map[string]string` holds one of them.

**Which one survives matters.** The ACL renders first, so the survivor is the half
whose absence lets strangers on rather than the half that merely fails to redirect.
**Cisco cannot be served on this path today**, and the collapse names what it drops
rather than joining the two into a value neither end would parse. The encoding belongs
to `internal/raddb`; this is recorded as a constraint, not a defect in the profile.

### A measurement can promote the encoding without promoting the behaviour

Reading vendor ids out of FreeRADIUS's dictionaries settles half of a claim and none
of the other half. There are **two facts of different standing about the same bytes**:

| Fact | Standing | What settles it |
|---|---|---|
| `Cisco-AVPair` is vendor **9**, attribute **1** | **DOCUMENTED** | the dictionary the server parses |
| A Cisco NAS **honours** `subscriber:command=reauthenticate` | **UNVERIFIED** | a device, and nothing else |

**Recording them as one rounds to whichever is louder, and the loud one is the wrong
one** — an encoding that is right buys nothing if the device ignores the value inside
it. So encoding evidence and capability evidence are kept in separate maps, and two
tests must **disagree** about the same vendors: every emitted attribute's encoding
DOCUMENTED, every capability still UNVERIFIED. Promoting a capability on the strength
of a dictionary read fails both.

The encoding test also requires each attribute to name what remains unmeasured, so a
promotion cannot delete the caveats along with the label.

For MikroTik's rate limit the vendor id and attribute number are DOCUMENTED; the value
format and the direction of the rate pair are not. The direction is the worst of the
four to be wrong about: **a swapped rate pair is applied, reported as applied, and
throttles the wrong way** — only a throughput test in both directions can see it.

### Vendor ids and attribute numbers, read from FreeRADIUS's own dictionaries

Read from `/usr/share/freeradius/dictionary.*` **inside the lab image the server
actually runs** — DOCUMENTED against the authority the server itself parses.

| Vendor | Vendor id | Attribute | Number |
|---|---|---|---|
| Cisco | **9** | `Cisco-AVPair` | **1** |
| Aruba | **14823** | `Aruba-User-Role` | **1** |
| Aruba | 14823 | `Aruba-AVPair` | 67 |
| MikroTik | **14988** | `Mikrotik-Rate-Limit` | **8** |
| MikroTik | 14988 | `Mikrotik-Address-List` | 19 |

**Why it matters.** A wrong vendor id or attribute number is *a valid
attribute the device ignores* — the NAS answers **ACK** and does nothing. It arrives
through the one channel a typed `Error-Cause` cannot see, so no amount of correct CoA
error handling would surface it.

**What is still a guess** is everything the dictionary does not carry: the **MAC
spelling** each platform matches a session on (Cisco `aabb.ccdd.eeff`, Aruba
unformatted lowercase, MikroTik colon-uppercase), the **AV-pair command strings**
including `reauthenticate-type=rerun` against `last`, and whether a NAS **NAKs 503**
for an unknown session rather than ignoring it. A device that ignores what it cannot
match is silence, and no harness here produces silence.

### `rlm_pap` cannot verify argon2

Measured on FreeRADIUS 3.2.10.

`rlm_pap` has **no argon2 scheme**. `Crypt-Password` is handed to libxcrypt, which does
bcrypt, yescrypt and sha512crypt and returns its failure token `*0` for `$argon2id$`.

EAP-TTLS/PAP gives a TLS tunnel and a verifiable cleartext inner password; what it
cannot do is verify that password against argon2id, because the thing doing the
verifying is `rlm_pap` ([`docs/rules/security.md`](rules/security.md) §1).

**What was measured to work** is a **second, opted-in verifier in a scheme `rlm_pap` accepts** — yescrypt or
bcrypt in `Crypt-Password` — completes EAP-TTLS/PAP **and, in the same run, does not
make MSCHAPv2 work.** Same shape as `account_nt_hashes`, read by `rlm_sql` from a
minimally-privileged view, with nothing crossing the `rlm_rest` boundary in either
direction. The price is that the 802.1X verifier is not argon2id. Foyer ships
**bcrypt** for it, because yescrypt is reachable only through `crypt(3)`, which a
`CGO_ENABLED=0` binary cannot call ([`docs/rules/security.md`](rules/security.md) §1).

**PEAP-MSCHAPv2 is blocked more firmly.** MSCHAPv2 needs the NT hash in the clear at
request time; `rlm_sql` can read the ciphertext and cannot decrypt it; §2 forbids
`rlm_rest` supplying it. The only shape that works is a **second declared cleartext
projection**, which [`docs/rules/database.md`](rules/database.md) calls a defect in advance.

### An argon2 hash in `Password-With-Header` becomes the password

**The most dangerous behaviour recorded in this file.** `rlm_pap` finds no `{scheme}`
prefix on an argon2id string, logs *"No {...} in Password-With-Header, re-writing to
Cleartext-Password"*, and then compares the submitted password **against the encoded
hash**.

So a one-way verifier becomes password-equivalent: anyone holding the hash
authenticates by sending it. Proven with an `eapol_test` run that authenticated by
submitting the stored verifier.

**A test asserting "the wrong password is refused" is green throughout.**

### `-XC` exit 0, server starts, and every EAP request fails

`ttls { virtual_server = "does-not-exist" }` — `radiusd -XC` **exits 0**, the server
**starts**, and it **answers Status-Server**. Every EAP request then fails at run time.

**Gate 2 cannot see it either**, because Status-Server never enters the `eap` module.
That is the sharpest available argument for the `eapol_test` suite: gates 1 and 2 are
both blind to an EAP configuration error, and only gate 3 is not.

Two more silently discarded, both accepted by `-XC` with exit 0 and absent from its
echo: `cache { redis_server = … }`, and **`tls-config { virtual_server = … }`** — the
hook much documentation names for certificate-based authorization, which exists in
FreeRADIUS **4**, not 3.2.

### The TLS session cache has no Redis backend in 3.2.x

Making the restarts that do happen survivable ([`docs/rules/radius.md`](rules/radius.md)) cannot
use Redis: the cache is internal to `rlm_eap_tls`, **not an `rlm_cache` instance**, and
a Redis item there is discarded with `-XC` exit 0.

`persist_dir` is the mechanism that exists and it works — measured, `<id>.asn1` and
`<id>.vps` on disk. It ships **off by default**, because a resumed session **skips the
method** and the directory holds the TLS master secret.

### `rlm_rest` silently discards configuration it does not recognise

Measured on FreeRADIUS 3.2.10. **This is the most
misleading behaviour recorded in this file**, because the config file and the running
server disagree and nothing warns you.

`verify_peer` is **not a configuration item `rlm_rest` has**, and a module-level
`tls { }` block is **discarded without a warning**. `radiusd -XC` printed the whole
`rest { }` section back as four items with no `tls` among them.

| The file said | What was actually in force |
|---|---|
| `tls { verify_peer = no }` | `check_cert = yes`, `check_cert_cn = yes` — **the opposite** |
| `timeout = 2` | `4.000000` — the per-section default, **above the ~3 s NAS retransmit interval** |

So a `tls { verify_peer = no }` line is inert, and the defaults behind it are
stricter than it claims. **The file misstates what is in force in both directions,
and `-XC` tells you if you read its output rather than its exit code.**

The **5.10 s** degraded answer recorded for the SIGSTOP test is the 4 s `timeout`
default plus `reject_delay`, not a handler limit plus a retry.

### `ca_file` is not the trust store, and both spellings parse

| Item | curl option | What it does |
|---|---|---|
| `ca_info_file` | `CURLOPT_CAINFO` | the trust store — **this is the one you want** |
| `ca_file` | `CURLOPT_ISSUERCERT` | pins the issuer of the peer's certificate |

**`radiusd -XC` prints either identically and accepts both.** `ca_file` alone fails
**every** request with CURLE 60. MEASURED.

### A healthy mTLS boundary logs the failure string too

`rlm_rest` opens the connection **before** applying the TLS options, so a working
deployment logs the same handshake-failure text once per pooled connection. A monitor
keyed on that string alarms on a healthy server. **The discriminator is the RADIUS
reply, not the log line.** MEASURED.

### `radiusd -XC` does not parse `thread pool` at all

The pool initialises after module instantiation, and **`-X` implies `-s`** — single
server, no pool. So a `thread pool` section that cannot be honoured **passes gate 1
unread**, which is one more reason [`docs/rules/radius.md`](rules/radius.md) says gate 1 is necessary
and not sufficient. Proven under `-f -xx`: `start_servers = 9` spawned nine threads and
`max_queue_size = 4096` printed back. MEASURED.

### `rlm_rest` return codes

| Fact | Value |
|---|---|
| HTTP 403 from the policy engine reaches unlang as | `userlock` |
| `userlock` in `authorize` | **returns from the section immediately**, so anything written after the module never runs |
| `pool { start = 5 }` with the control plane down | **FreeRADIUS refuses to start** — *"Instantiation failed for module rest"* |
| `pool { start = 0 }` | the server starts, and each request fails closed |

The first two together mean an `if/elsif/else` written after `rest` in `authorize`
**never executes on a denial**: it is unreachable and looks correct. PostgreSQL stays
a start-time dependency; the control plane must not be one, or an outage in Foyer
becomes an outage in RADIUS.

### The change classification, both halves

| Class | Measured behaviour |
|---|---|
| `sql-only` | Before the INSERT, packets are discarded as an unknown client. After it: `Access-Accept`, and the server logs `Adding client 172.18.0.77/32`. **No restart, no signal.** |
| `restart` | Rotating the secret of a client the server loaded at startup leaves the running server on the old one — *"invalid Message-Authenticator! (Shared secret is incorrect.)"* |

### EAP

`eapol_test` (Debian `eapoltest` 2.10) against the generated configuration:
`CTRL-EVENT-EAP-SUCCESS` … `SUCCESS`, and the server logs
`Login OK: [foyer-eap-tls-probe]`. **EAP-TLS only** — every other method is
UNVERIFIED, and behaviour against a real supplicant is unmeasured.

**The TLS session cache is off unless enabled, and then persists with `persist_dir`**
(above); 3.2.x has no Redis backend for it. The generated `eap` module says so where
it configures the cache.


### `radpostauth`, and the two things the stock configuration gets wrong for us

Read from the shipped v3.2.x tree —
`raddb/mods-config/sql/main/postgresql/schema.sql` and `queries.conf`. DOCUMENTED.

The stock table:

```sql
CREATE TABLE IF NOT EXISTS radpostauth (
    id bigserial PRIMARY KEY, username text NOT NULL, pass text, reply text,
    CalledStationId text, CallingStationId text,
    authdate timestamp with time zone NOT NULL default now(), Class text
);
```

The stock `post-auth` query that fills it:

```
INSERT INTO ${..postauth_table} (username, pass, reply, authdate ${..class.column_name})
VALUES('%{User-Name}', '%{%{User-Password}:-%{Chap-Password}}', '%{reply:Packet-Type}', '%S.%M' …)
```

| Fact | Status | Consequence for Foyer |
|---|---|---|
| The stock query writes **`'%{%{User-Password}:-%{Chap-Password}}'` into `pass`** — the guest's submitted cleartext password, on **every** attempt, accepted or rejected. | DOCUMENTED | **Foyer's generated query must never emit the `pass` column.** This is a credential store nobody asked for, in a table an admin console is about to read. [`docs/rules/security.md`](rules/security.md) §1. |
| The stock query **does not populate `CalledStationId` or `CallingStationId`**, although the columns exist. | DOCUMENTED | A stock deployment records **no MAC** on any authentication attempt, so the only identifier a café operator has — the one on the guest's phone — is absent. Foyer's generated query must populate both, and the contract carries `MacCoverage` so a console can say when they are not there. |
| `radpostauth` has **no reason column**. `reply` holds `Access-Accept` or `Access-Reject` and nothing more. | DOCUMENTED | A RADIUS reject can never say *why*. The `AuthEventKind` enum therefore has one `radius-reject` and no `radius-reject-because-*`, and the console renders a note saying the reason was never observable rather than "unknown". |
| A **wrong shared secret** produces no `radpostauth` row at all — the request is discarded before post-auth runs. | DOCUMENTED | The most common real cause of *nobody can get online* is invisible in this table. Hence `AuthBlindSpot`: the API states what it cannot see, so an empty result cannot read as "nobody tried". |

| Fact | Status |
|---|---|
| `require_message_authenticator` defaults to **`auto`**, which silently leaves legacy gear exposed to BlastRADIUS (CVE-2024-3596). FreeRADIUS recommends `= true` on all clients plus `limit_proxy_state = yes`. | DOCUMENTED — freeradius.org |
| `radiusd -XC` checks are **incomplete**: it "can return a zero status code … but may still exit with an error when run normally," and some modules are skipped entirely | DOCUMENTED — `radiusd(8)` |
| `rlm_rest` runs libcurl **synchronously on the request thread**; the pool is fixed (`max_servers`), and Status-Server is served by the same threads — so a stalled REST backend blocks the health check too | DOCUMENTED — freeradius-users |
| `rest` and `sql` are callable in the same virtual server; `rlm_rest` takes per-section config (`authorize`, `accounting`, `post-auth`, …) with distinct URI and method | DOCUMENTED — `raddb/mods-available/rest`, v3.2.x |
| SIGHUP does not reliably reload clients, listeners or virtual servers in 3.2.x; a restart drops in-flight EAP and the in-memory TLS session cache | DOCUMENTED |
| 4.0-alpha implements **no PEAP, TTLS, EAP-FAST or TEAP**, and no RadSec | DOCUMENTED — freeradius.org/4.0/alpha.html |
| Reverse CoA in 3.2 requires the non-default `WITH_COA_TUNNEL` build flag and is beta-quality | DOCUMENTED — draft-ietf-radext-reverse-coa-08 |

---

## Open unknowns

| Unknown | Why it matters | How to resolve |
|---|---|---|
| Which `radacct` columns does each NAS actually populate? | Every admin screen showing session detail depends on it | Accounting from a real NAS of each family, recorded in *Baseline facts* |
| **Does the Omada Open API provide a client presence check, and under which credentials?** | [`security.md`](rules/security.md) §4 makes `VerifyPresence` mandatory, and the Omada external-portal API has none. **This can block the driver, not just a screen.** | Read the Open API spec on a running controller; confirm against a device |
| Which controller version floor applies per vendor? | A registration-time check that fails loudly beats a 2 a.m. support call. UniFi is sourced (Network Application ≥ 9.1.105 for the Integration API); Omada and Ruckus are not | Vendor release notes plus one controller each |

### Client-environment unknowns — each is a cheap on-device measurement

Desk research does not answer these questions, and they are the reason testing the
portal on a real phone, inside a real CNA, is not skippable. Every one is
minutes of work with a phone in hand.

| Unknown | Why it matters | How to resolve |
|---|---|---|
| Does the iOS CNA auto-dismiss on `captive: false`? | **No public source exists.** Decides whether the success page needs a "you may close this" instruction | Phone + `lab/` portal |
| What is the CNA's timer-based recheck interval? | Bounds how long a user stares at "Cancel" after a successful login | Stopwatch |
| Does the CNA still break `localStorage`/`sessionStorage` on current iOS? | The claim traces to a single 2017 post. Decides whether client-side language preference can persist within a session | try/catch test write on-device |
| Does the CNA honour `prefers-color-scheme` and `env(safe-area-inset-*)`, and what is its viewport on iPhone? | Dark mode and the bottom CTA depend on it. macOS is 900×550 pt; on iOS an on-device page **reported** a 375 × 642 CSS px viewport, light scheme and zero insets — read from a screen, not a log, so not yet MEASURED | On-device page that posts its `matchMedia`/`env()` digest to the server |
| Does iOS still use the legacy probe on first association? | Measured true on iOS 14–16; unmeasured on later releases | `tcpdump` on the gateway |

### Answered by measurement

| Question | Answer | Standing |
|---|---|---|
| Does UniFi support changing limits on a **live** session without re-authorization? | **Yes** — see *The controller is the object* | MEASURED |
| Does the UniFi Integration API report per-client byte counters? | **Yes, on the detail endpoint only** — see *Not every session figure dies with accounting* | MEASURED |
| Does a container behind a published UDP port see the real NAS source address, or the Docker bridge gateway? | **The real address** for traffic from another host and for traffic the host sends to its own LAN address (stock daemon, `userland-proxy` at its default). **Only traffic the host sends to its own loopback** (`127.0.0.1:1812`) arrives as the bridge gateway — so test from another machine, or from the host's LAN address, never from `127.0.0.1` | MEASURED |
| Does same-origin `@font-face` load in the iOS CNA? | **Yes**, over HTTP and over TLS, both `.ttf` and `.woff` (server log) | MEASURED |
| Does the iOS CNA display a portal served with a **self-signed** certificate? | **Yes, after one warning and one tap.** Every subresource then loads over TLS 1.3. The exception is stored against the *certificate*, not the network — forgetting and rejoining the SSID did not bring the warning back. Plain HTTP renders identically, labelled "Not Secure" in the CNA's chrome. Whether the exception survives a reboot, an iOS update or a regenerated certificate is UNVERIFIED | MEASURED |

**When equipment teaches you something, the deliverable is a new `lab/` test plus a
line in this file** — not a fix alone.
