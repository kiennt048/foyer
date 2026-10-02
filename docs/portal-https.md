# The guest portal over HTTPS, and RFC 8908

For the operator of a Foyer deployment. What the portal does by default, how to
give it a certificate, and what the operating system's captive-portal API
(RFC 8908) can and cannot do on your network.

Every claim is marked **MEASURED** (run in this repository's tests, or on real
equipment where stated), **DOCUMENTED** (a cited RFC or vendor source) or
**UNVERIFIED** (still needs a real phone, a real controller or a real host).

---

## The default is plain HTTP, and that is a decision

The portal answers on HTTP (published on 80 and on UniFi's 8880). This is deliberate:
a self-signed certificate costs every guest one warning on their first screen, and
what HTTP exposes on an open SSID — a voucher code read out of the air — buys an
attacker an afternoon of Wi-Fi. **The captive portal is a billing and consent
boundary, not a security boundary.**

What that means, stated plainly: **voucher codes and guest-account passwords cross
the air unencrypted on an open SSID.** If your guests log in with an account whose
password they may reuse elsewhere, give the portal a certificate.

## Giving the portal a certificate

Foyer never generates a portal certificate and never ships one. You supply the pair,
**in the admin console**, and nothing restarts. Signed in as an administrator
(the screen needs `nas:manage`; staff can see it and not change it):

1. Choose the certificate (next section) and the address guests will use — a DNS
   name, or the host's IP address.
2. Open **Network** in the console. Under **Portal certificate**, choose **Upload a
   certificate** and paste — or **Read from a file** — the certificate PEM (the leaf
   first, then any intermediates) and its private key (PEM, not passphrase-protected).
   Put the `https://` address in **Portal address to serve it at**; it is saved with
   the certificate, because the two have to agree.
3. **Upload certificate.** It is checked before anything is stored — it parses, the
   key is the certificate's, the chain is in order, it is valid today, and it names
   the portal address — and a refusal says which of those failed. On success it is
   served from the next TLS handshake. The private key is sealed on the way into the
   database and is **never shown again**: the screen says *Set, and never shown*.

The same card shows what is being served: its names, its expiry and its SHA-256, or
*Stored, and not served* with the reason (the portal address is not `https://`, the
certificate does not name it, it has expired, or the HTTPS listener could not start).
The **Portal address** card above it sets the address on its own, and the optional
**Venue information page** (RFC 8908's `venue-info-url`). Both are written to the
audit log (`portal-certificate-changed`, `network-settings-changed`).

**Removing the certificate:** **Remove certificate** on the same card. The portal goes
back to plain HTTP from the next request, and the plain listener stops redirecting.

### The installation's own pair — install time only

An installation may still carry a pair on disk, and that is what an **automated
renewal** (an ACME client) writes to: `/opt/foyer/secrets/portal-tls/portal.crt` and
`portal.key`, which `deploy/first-boot.sh` wires in as `FOYER_PORTAL_TLS_CERT` /
`FOYER_PORTAL_TLS_KEY` when it finds them, together with the `https://` address given
to it at install (`FOYER_PORTAL_URL`). These are **bootstrap values, set once by the
installer, never an operator's setting**: a certificate or address saved in the
console wins over them, and the console labels the one in force *From the
installation*. So pick one source per install — uploading in the console, or the
renewal hook's files — because a console upload shadows the files until it is
removed.

`FOYER_PORTAL_TLS_ADDR` (default `0.0.0.0:8444`, published by `deploy/compose.yaml`
on 443 and 8843) is the listener's bind address and is not a setting at all.

### What changes when a certificate is configured — MEASURED

| | |
|---|---|
| HTTPS listener | serves the whole portal: front door, pages, `/api/v0/portal/*`, venue photos, RFC 8908 |
| HTTP listener | **still bound** — it is where the access point's redirect lands — and answers every GET with `302` to the same path and query on the `https://` origin of the portal address. The NAS parameters (`id`, `ap`, …) are relayed untouched |
| A form posted over plain HTTP | refused with `403`, never re-sent to HTTPS (the body already crossed the air once) |
| Session cookie | minted on the HTTPS side, `Secure` |
| `Strict-Transport-Security` | **never sent**, on purpose — see below |
| Refusal | a console upload that does not parse, match its key, chain, cover the portal address (IP SANs count) or fall inside its validity is refused with the reason and nothing is stored; an installation pair that does not load or cover the host refuses start-up |
| Renewal | upload the new pair in the console — in force at the next handshake, no restart. An installation pair is re-read when its files change, at the next handshake; a replacement that does not load or does not cover the host is refused and the previous certificate kept, with a warning in the log |
| Expiry | shown on the console's certificate card, logged at start with a warning inside 14 days, and asserted by `deploy/verify.sh`. **Nothing in Foyer renews it** |
| Checked on the host | `deploy/verify.sh`, section *Guest portal HTTPS*: with no certificate, 80 serves guests and neither 443 nor 8843 completes a handshake; with one, 80 redirects to `https://` and both present an unexpired certificate covering that host |

Tests: `cmd/foyer` `TestThePortalServesHTTPSWhenACertificateIsConfigured`,
`TestTheHTTPPortalSendsGuestsToHTTPSOnceACertificateIsConfigured`,
`TestAPortalCertificateForAnotherNameIsRefused`,
`TestAReplacedPortalCertificateIsServedWithoutARestart`; `internal/config`
`TestThePortalTLSConfigurationIsRefused`.

**Renewal hooks** (installation pair only) **must keep the files readable by the container.** `first-boot.sh`
hands both files to the control plane's uid (the key `0400`). An ACME client that
writes its renewal as root, `0600`, produces a pair the process cannot open: it is
refused, the previous certificate keeps being served, and the portal reaches that
certificate's expiry with nothing but a log line. Write the files atomically
(rename into place — a half-written pair is refused and retried at the next
handshake, so the order of the two does not matter), and either re-run `first-boot.sh` or `chown` them
to that uid from the hook. (UNVERIFIED with a real ACME client on a real host.)

**Retiring an installation pair** is an install-time change, not an operator one:
the installer takes both files out and re-runs `first-boot.sh` with an `http://`
address, which keeps the URL it already has and warns — rather than refuses — when
that URL is `https://` with no pair, because a TLS-terminating proxy in front of the
portal is a deliberate shape. An address set in the console overrides it either way.

### Which certificate

| | Publicly trusted, for a DNS name | Self-signed, for the host's IP |
|---|---|---|
| Guest sees a warning | no | **yes, once per device per certificate** (MEASURED, iOS 18) |
| RFC 8908 usable by the OS | yes, if the name matches the DHCP option 114 URI | **no** — RFC 8908 §4: the client MUST NOT proceed on a certificate it cannot validate (DOCUMENTED); Android falls back silently (DOCUMENTED, AOSP) |
| Needs | the name to resolve **before** authentication, to the portal's address | nothing |
| Works with a NAS that redirects by IP | only if the NAS is configured with the name instead | yes — the iOS CNA connected by IP, with no SNI (MEASURED) |

`docs/protocol-reality.md` records the consequence: *a LAN appliance with no public
DNS name cannot serve RFC 8908 at all.* The interception path is how guests find the
portal, and it stays.

### Why the portal never sends HSTS

The field's documented pitfall (Fortinet, Aruba, PacketFence, pfSense and UniFi
community threads all describe it): a browser holding HSTS for the site the guest
*asked for* never follows an interception to anything — which is why the
interception is always HTTP. On the portal's own name, HSTS would turn the day the
certificate lapses, or you decide to go back to HTTP, into a hard failure with no
click-through on every phone that ever visited, and nothing you can do on the
server undoes it. Browsers ignore HSTS on an IP literal anyway.

### The walled garden

Before authentication the device must be able to reach ([`docs/rules/security.md`](rules/security.md) §5):

- the portal's name or address on **443 and 8843** as well as 80 and 8880;
- **DNS** resolving that name to the portal, if you use a name;
- the **OCSP / CRL endpoints** of the certificate's chain, if your CA publishes
  them — an operating system that checks revocation before it trusts the page
  needs them pre-authentication (UNVERIFIED which OS versions block on it);
- the RFC 8908 endpoint, which is on the portal itself.

### UniFi

UniFi redirects a guest to `http://<portal>:8880/guest/s/<site>/`, and to
`https://<portal>:8843/…` when the controller's HTTPS redirect is on (MEASURED on a
UniFi access point — the `redirect_https` site setting).
With a certificate, either works: 8880 is redirected by Foyer, 8843 is served
directly. The controller's portal host must be the name or address the certificate
covers. **UNVERIFIED end to end on real UniFi with Foyer serving HTTPS.**

---

## RFC 8908: the operating system's captive-portal API

`GET /api/v1/captive-portal` on the portal, `application/captive+json`. Point DHCP
option 114 on your gateway at `https://<name>/api/v1/captive-portal`.

### What it answers — MEASURED

- `captive: false` for the device that holds a live grant, with `seconds-remaining`
  when the grant has an end;
- `captive: true` for everybody else, and for a grant that has expired, been
  revoked, or whose network the controller never opened;
- `user-portal-url` = the portal address (console: **Network → Portal address**);
  `venue-info-url` only if you set **Venue information page** there;
- never `bytes-remaining` (Foyer holds the plan's allowance, not what was used —
  usage arrives from accounting every five minutes at best, and not at all on the
  controller path) and never `can-extend-session`.

Tests: `internal/portal` `TestTheCaptiveAPI…`, `cmd/foyer`
`TestTheCaptivePortalAPIAnswersForTheDeviceThatAsked`, and against PostgreSQL
`internal/apistore` `TestTheCaptivePortalAPIAnswersFalseAfterARedemptionInPostgreSQL`.

### How it knows which device is asking, and what that requires of your network

The operating system's API client is not the portal page and carries no cookie.
RFC 8908 §4 expects the server to recognise the device by **IP address** — it
requires *"the same IP addresses visible to both the API server and the
enforcement device"* (DOCUMENTED). Foyer remembers which session each address
reached the portal from, and answers from that session's grant.

So:

- **No NAT and no proxy between the guests and the portal.** Every guest behind one
  address is indistinguishable. Foyer detects a second device on one address and
  then answers `captive: true` for it — the old answer, never another guest's
  grant. `X-Forwarded-For` is deliberately ignored: it is a header anybody can send.
- **Docker must preserve the source address.** It does for IPv4 published ports
  with its default rules (MEASURED from another host on the LAN — traffic the host
  sends to its own loopback is the exception and arrives as the bridge gateway); the
  userland proxy used for some IPv6 setups does not.
- **A known residual risk.** If the DHCP server hands a departed guest's address to
  a new device while the old grant is still live, that new device's operating
  system is told `captive: false` until it reaches the portal. DHCP servers avoid
  reusing a leased address within its lease, so this needs pool exhaustion; keep
  the guest pool larger than the peak number of devices.
- **Kept in the database, for a day.** Which device was last seen from which
  address is stored (`portal_addresses`), so a restart of the control plane, or a
  second control-plane process, still answers a device that is online. An entry
  goes a day after its address was last seen, and at once when the device is erased.
  If the database cannot be read, the answer is `captive: true`.
- **Without a certificate** the endpoint still answers, over HTTP, but RFC 8908
  requires an `https` URI and an operating system is expected to ignore an `http`
  one. Nothing breaks: devices find the portal by interception, as they always have.

What the operating systems do with it (DOCUMENTED, `docs/protocol-reality.md`):
iOS 14+ and Android 11+ read `captive` and `user-portal-url`; iOS renders nothing for
`seconds-remaining`; Windows and Linux desktops do not implement RFC 8908.
