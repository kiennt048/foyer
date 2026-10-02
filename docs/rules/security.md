# Security rules

> Governs everything under `internal/`. Read it before changing anything there.

Foyer authenticates access to networks. Assume every change is a security change.
Code across the tree cites this file by section number (`docs/rules/security.md §4`);
the numbers are stable.

## 1. Credential storage — the constraint that shapes the schema

Challenge-response protocols force the server to hold recoverable material. This
is physics, not a design preference:

| Method | Server must hold | argon2 possible? | what Foyer stores |
|---|---|---|---|
| PAP (portal, console) | anything | **yes** | argon2id |
| **EAP-TTLS/PAP (802.1X)** | anything `rlm_pap` can read | **no — see below** | **bcrypt, opted in per identity** |
| CHAP | cleartext | no — **not supported, do not add it** | nothing |
| MSCHAPv2, PEAP-MSCHAPv2 | NT hash (unsalted MD4) | no | NT hash, enveloped, opted in |
| EAP-TLS | nothing | n/a | nothing |

**The 802.1X row is a deliberate trade-off and it must not be softened anywhere it is
written.** `rlm_pap` in FreeRADIUS 3.2.10 has **no argon2 scheme**, and its general
door — `Crypt-Password` — hands the string to libxcrypt, which returns its failure
token `*0` for `$argon2id$`. Measured, not reasoned (see
[`docs/protocol-reality.md`](../protocol-reality.md)). So **an identity that
authenticates to the network with a password over RADIUS (a NAS's PAP login, and
EAP-TTLS/PAP when it is emitted) is verified against bcrypt, not argon2id**,
and a console that calls it argon2 is describing something the data plane cannot do.

Three properties bound that, and none of them is *"bcrypt is fine"*:

- **It is a second verifier, opted in per identity.** `accounts.password_hash` is
  still argon2id for everybody, and it is still what the portal and the console
  verify against. The bcrypt row answers FreeRADIUS and nothing else — 802.1X, and a
  NAS's plain PAP login too: a guest account over RADIUS is refused unless it has
  one, because `rlm_pap` cannot check argon2id and a username alone is not a
  credential.
- **It is one-way.** Unlike an NT hash it is *not* password-equivalent — reading it
  does not authenticate — which is the entire reason it may live in a column
  `rlm_sql` can read, and the reason EAP-TTLS/PAP is the planned 802.1X password
  method where PEAP-MSCHAPv2 is refused.
- **The 802.1X username is globally unique**, because FreeRADIUS's identity
  namespace is flat. Two opted-in identities sharing a username make the credential
  query ambiguous, and that failure *succeeds*: one site's password authenticates
  another site's identity. Migration `00016` refuses it and names the collision.

Why bcrypt rather than yescrypt, which has the same property: the existing dependency
`golang.org/x/crypto` carries bcrypt and no yescrypt (new dependencies are discussed
in an issue first), and `deploy/Dockerfile.foyer`
builds the control plane `CGO_ENABLED=0` on alpine — so `crypt(3)`, the only
implementation of yescrypt on the machine, is not callable from this process at all.
Both schemes were measured to complete EAP-TTLS/PAP through `rlm_pap`.

**Rules:**

- Admin accounts and portal/guest accounts are **argon2id, always**. They never
  need MSCHAPv2. Do not put them in the same table as 802.1X credentials.
- NT hashes live in a **separate table, envelope-encrypted under a key that is
  not in the database**, and only for identities explicitly opted in to
  PEAP-MSCHAPv2.
- An NT hash is **password-equivalent**, not a hash you can shrug at: it is the
  sole input to the MSCHAPv2 response, so a stolen hash authenticates directly.
- **EAP-TLS is the corporate default, and it is the only EAP method the generator
  emits today.** `Generator.Validate` refuses `ttls` and `peap` by name.
  **EAP-TTLS/PAP is the planned second method**: it is the one combination giving a
  TLS tunnel *and* a verifiable cleartext inner password.

  **It does not give you argon2.** `rlm_pap` in 3.2.x has no argon2 scheme, and
  `Crypt-Password` goes to libxcrypt, which returns `*0` for `$argon2id$`. The way
  through is a **second, opted-in verifier in a scheme `rlm_pap` accepts** — bcrypt —
  which can complete TTLS/PAP and **does not make MSCHAPv2 work**. The verifier exists:
  migration `00016`, `account_pap_verifiers`, `identity.HashPAPVerifier`, and the view
  `radius_pap_verifiers` — two columns, readable by `foyer_radius` only, disabled
  accounts absent. It answers a NAS's plain PAP login today; the generator does not
  yet emit an EAP-TTLS method that uses it.

  **And never put an argon2 hash in `Password-With-Header`.** `rlm_pap` rewrites it to
  `Cleartext-Password` and compares the submitted password against the encoded hash, so
  the verifier becomes the password — and a test asserting that a wrong password is
  refused stays green. authentik reached the same conclusion about MSCHAPv2 and dropped
  PEAP-MSCHAPv2 entirely.
- **Never add CHAP.** It requires cleartext storage for a legacy checkbox.

### The generated `post-auth` query must not write `pass`

Stock FreeRADIUS 3.2.x inserts `'%{%{User-Password}:-%{Chap-Password}}'` into
`radpostauth.pass` on **every** authentication attempt, accepted or rejected. That
is a cleartext credential store, populated by default, in a table the admin console
reads — and it exists in the one place nothing else in this repo guards, because
`radpostauth` is FreeRADIUS's table and the rule everywhere else is *do not write
it*.

**We generate the query, so this is ours.** Emit `username`, `reply`, `authdate`,
`CalledStationId` and `CallingStationId`. **Never `pass`.** There is a snapshot test
on the generated config for the same reason there is one for
`require_message_authenticator`.

Two corollaries, both load-bearing:

- **Populate the station-id columns.** The stock query leaves them empty even though
  the schema has them, so a stock deployment records no MAC on any attempt — and the
  MAC is the only identifier an operator has when a guest is standing in front of
  them. `AuthAttempt.callingStationId` and the `MacCoverage` field exist because of
  this.
- **Nothing downstream may echo a submitted credential**, which is the same rule the
  portal already carries for the username echo. `AuthAttempt` has no field a password
  could arrive in, and there is a mock test asserting no key on the object matches
  `/pass|secret|credential/`. The schema is the first gate; the test is the second.

## 2. Credentials never cross the rlm_rest boundary

`rlm_rest` carries **authorization decisions only**. FreeRADIUS fetches password
verifiers itself via `rlm_sql` from a minimally-privileged view.

**An allow is not an authentication.** It says what it authorized
(`Foyer-Identity-Kind`: `voucher` or `account`), and the generated tree accepts on
its own only for a voucher; an account goes through `rlm_pap` against
`radius_pap_verifiers`. An allow that set `Auth-Type := Accept` for every identity
would make a guest account's username alone enough to get online.

**The question is built from client-chosen values, so an unreadable one is the
client's doing and is refused** — never routed to the failure branch, which at a
fail-open site is an accept. The two identities travel as `%{hex:&Attr}`
(`user_name_hex`, `outer_user_name_hex`) and every other value is `%{jsonquote:}`d
— jsonquote rewrites every byte >= 0x80 on the amd64 build, so a non-ASCII name
would otherwise arrive as a different name. The Policy API refuses duplicate keys (by
exact name), unknown fields, trailing data, bad hex and invalid UTF-8 (measured on
3.2.10; [`internal/raddb/CONTRACT.md`](../../internal/raddb/CONTRACT.md) §12).

If you ever find yourself returning `control:Password.NT` or
`control:Password.Cleartext` from the Policy API, stop. That turns the Policy API
into an endpoint that yields the credential for any username supplied — a full
credential-store dump without touching Postgres.

Also required on that boundary:

- **mTLS.** FreeRADIUS and Go are separate containers on a shared bridge network,
  not loopback.
- The Policy API **refuses non-TLS on any non-loopback address** — a startup
  assertion, not a doc note.
- Timeouts below the NAS retransmit interval (~3 s): `connect_timeout` ≈ 0.5 s,
  `timeout` ≈ 1–2 s. `rlm_rest` blocks a FreeRADIUS worker thread; the pool is
  fixed (`max_servers`), and when it is exhausted **Status-Server is blocked too,
  so the health check dies with the thing it checks.**
- Failure behaviour is explicit and per-site: **fail closed for 802.1X**,
  configurable fail-open for guest. Never leave it to unlang's default.

## 3. RADIUS shared secrets and BlastRADIUS (CVE-2024-3596)

An on-path attacker can forge an Access-Accept from a genuine Access-Reject using
MD5 chosen-prefix collisions. FreeRADIUS's default
`require_message_authenticator` is **`auto`** — which exists for legacy
compatibility and therefore silently leaves the weakest gear unprotected.

**Because we generate the client definitions, this is our responsibility:**

- Emit `require_message_authenticator = yes` and `limit_proxy_state = yes` in
  **`security {}` globally, in every network section, and in the dynamic-clients
  site** — three places, each pinned by a snapshot test.

  **Not per client, because it cannot be.** `rlm_sql` reads exactly
  `id, nasname, shortname, type, secret, server`; there is no seventh column and
  `client_query` cannot carry one, so once clients come from SQL — which
  [`radius.md`](radius.md) requires — a per-client setting has nowhere to live.
  Measured on 3.2.10.

  The guarantee ends up **stronger**: a global `yes` cannot be weakened by a row in
  a table. But `nas.require_message_authenticator` and `nas.limit_proxy_state` exist
  in the schema and are **inert** — columns that look like switches and are not.
  Treat either as a control and you will configure nothing.
- A NAS that cannot do it produces a **loud, per-NAS warning in the admin UI**.
  Never silently downgrade.
- Secrets: per-NAS, `crypto/rand`, **no default ever** (FreeRADIUS ships
  `testing123`), envelope-encrypted at rest, write-only in the admin API.
- **Redact secrets from logs and from `foyerctl support-bundle`.** `radiusd -X`
  output contains them in cleartext, and there will be a "send us your logs"
  button.

## 4. Never trust the NAS redirect

UniFi passes `ap`, `id`, `t`, `url`, `ssid` as **unsigned query parameters**.
Anyone can call the portal with values of their choosing.

`NASDriver.VerifyPresence()` is **mandatory before creating a grant**: confirm
the MAC is really associated, on the claimed AP, in the claimed site, and still
unauthorized. Without it the portal is a public API for authorizing arbitrary
MACs — including devices that never associated.

Bind the portal session cookie to the MAC; reject a mid-flow MAC change.

**Open redirect:** the NAS passes the original URL. Scheme allowlist
(`http`/`https` only), no protocol-relative or userinfo URLs. Prefer rendering a
success page with the destination as a link over auto-redirecting — the victim is
on an untrusted network, *expects* to be redirected, and has just typed
credentials.

## 5. The walled garden is unauthenticated egress

- Enforce by **FQDN/SNI where the driver supports it, never by CDN IP ranges** —
  "allow the OAuth provider" degrades into "allow a general-purpose HTTPS tunnel"
  because CDNs host arbitrary customers.
- **Quota the pre-auth state**: per-MAC byte cap, rate limit, time limit. There
  is no identity yet, so nothing else bounds it.

  **Key the rate limit on what the caller cannot type.** A bucket keyed on the MAC
  in the query string and spent *before* `VerifyPresence` lets one laptop naming a
  hundred thousand random MACs fill the table so that every real guest after it is
  refused — the bound on memory becomes the denial of service. So: before presence,
  the key is the **connection source** (the TCP peer, an IPv6 peer on its /64), sized
  as a flood bound so a NATed venue still fits; after presence, it is the **verified
  MAC on that source**, so a stranger claiming a present guest's MAC spends their own
  budget. Both tables forget their least recently seen key when full rather than
  refusing newcomers (`ratelimit.NewEvicting`). PacketFence throttles its portal per
  client IP, and nginx's `limit_req_zone` drops its oldest state when full;
  [`internal/portal/ratelimit.go`](../../internal/portal/ratelimit.go).
- Restrict pre-auth DNS to the local resolver with size and rate limits. DNS
  tunnelling for free internet still works.
- The garden must contain the portal FQDN, its cert chain's OCSP/CRL endpoints,
  and the RFC 8908 endpoint — or the portal cannot serve itself.

## 6. MAC identity is weak — never pretend otherwise

802.11 addresses are cleartext regardless of WPA2/WPA3, and iOS 14+/Android 10+
present a private MAC per SSID by default. Bind grants to **identity**, with MAC
as a hint. Detect the locally-administered bit. Limit concurrency per grant.

Android's per-SSID address is **persistent** by default, and its non-persistent
mode is explicitly gated on a network that has *not* presented a captive portal —
so on a Foyer SSID the address is a stable pseudonym rather than churn. That makes
"remember this device" viable; it is still a locally-administered pseudonym, so it
**supports** this rule rather than relaxing it.

**`VerifyPresence` proves a MAC is on the network, not that the caller is that
device.** Reading it the wide way produces a family of defects: a rate limit, an SMS
limit or a consent withdrawal keyed on a MAC the caller named passes, because the
MAC's owner really is online. Anything that spends or changes **a particular
guest's** state — their budget, their consent — is keyed on what that guest's own
connection proves: the portal session that was granted (the cookie), or the MAC
together with the source it arrived from. A MAC alone, however verified, is a claim.

**The captive portal is a billing and consent boundary, not a security
boundary.** Say so in user-facing docs.

## 7. Secure defaults, because strangers deploy this

- **No static credentials anywhere in the repo.** First-boot generation of the DB
  password, signing keys and RADIUS secrets — and **no administrator password at all.**

  **The first administrator comes from a one-time setup, not a password.** A fresh
  install has no operator. The installer runs `foyerctl open-setup`, which stores the
  SHA-256 of an 80-bit random code in `console_setup` and prints the code; the console
  shows *create the administrator* while the setup is open, and `completeAdminSetup`
  turns the code plus a username and a password of the operator's own into the first
  administrator, **once**, in one transaction (two racing browsers: one administrator,
  one `409`). The setup closes permanently through the API; the installer can reopen it
  only where no enabled administrator exists. The code is throttled per address, never
  per account — one shared bucket would let a single device keep the venue's operator
  out. Why a code and not authentik's or UniFi's first-come page: the console is
  published on every interface. [`internal/api/handlers_setup.go`](../../internal/api/handlers_setup.go)
  has the survey (authentik, UniFi, Omada, Grafana, Jenkins, GitLab, Keycloak, OWASP
  ASVS §2.3.1/§2.5.4). Nothing in the tree may create an operator with a fixed
  password; `TestTheInstallerShipsNoPassword` reads the installer for it.

  **The console's sign-in lockout is split by device** (OWASP's device-cookie
  defence): a browser that signed in as an account before carries a signed
  `__Host-foyer_device` cookie and spends from its own bucket, so a stranger who
  empties the account's shared bucket refuses strangers, not the account's real user.
  **Its key ring is persisted, sealed under `FOYER_KEK`** (`device_cookie_keys`,
  migration 00053) and rotated by policy (ninety days, retired keys verifying for a
  cookie's lifetime) — never by a restart: a per-process key would untrust every
  device at each restart, which is exactly when a stranger already flooding the
  account locks the operator out. A lockout defence must survive the events that
  reset state. The **portal's** per-account bucket is per account *and* source
  address instead, because Apple's CNA destroys its cookies when the sheet closes.

  **Credentials for test equipment.** Credentials for equipment that exists only to
  be measured — a test controller, a test host, a borrowed access point — may be
  **kept on the contributor's own machine** (for example `~/.foyer/`, mode `0600`;
  `.secrets.local/` inside a checkout is gitignored for the same purpose).

  **They are never committed.** This repository is public and **git history is
  permanent**: a key committed today is still readable after it is deleted, by
  anyone who ever cloned. A test box is a real box on a real network, and "test" is a
  statement about intent rather than about what the credential opens.

  So the rule reads, in full: **a credential may live beside the work and never inside
  it.** Pass them with `-key-file` / `-password-file` or an environment variable rather
  than a flag, so they reach neither the tree nor shell history, and **scan the staged
  diff before committing**.

  Rotate them when the equipment stops being test equipment. Nothing here makes a
  credential safe; it makes it *local*.
- **Refuse to start** if a known-default value is detected. Default credentials
  in a compose file are the most common way appliance software gets mass-owned.
- **Never ship a CA in an image.** A fixed key means every deployment on earth
  shares one private key.
- Admin UI and Policy API on **separate listeners** from the guest portal.
- Publish nothing in `deploy/compose.yaml` that does not need publishing.

## 8. Inner identity only

In PEAP/TTLS the outer identity is client-supplied and arbitrary
(`anonymous@realm`). **Authorization keys only on the inner identity.** Any path
that reads the outer one lets an attacker choose their own authorization subject.
There is a contract test for this in `lab/`; do not weaken it.

## 9. Standard hygiene, stated because it gets skipped

CSRF on state-changing routes and on downloads (an export is a GET that hands
personal data out and writes an audit entry — `SameSite=Lax` does not stop a
cross-site navigation to it); CSP; secure/httpOnly/SameSite cookies; no stack
traces or internal paths in API errors; `crypto/rand` never `math/rand` for
codes, tokens and secrets; constant-time comparison for vouchers and OTPs;
single-use enforced transactionally, not with a read-then-write.

**OTP specifically:** the top risk is not brute force, it is **SMS toll fraud** —
attackers submit premium-rate numbers in volume and take a cut. Ship a hard
global spend cap that fails closed, a destination-country allowlist defaulting to
the install's own country, and per-IP/per-MAC/per-number limits. This is a
default-configuration decision, not a feature.

**Where each of those three lives:** the spend cap is a per-site daily message
ceiling counted in PostgreSQL in the same transaction as the code, refusing when it
cannot be counted (`phone_code_sends`); the allowlist is Vietnamese mobile ranges
(`otp.NormaliseVN`); the limits are 3/h and 5/day per number (SQL, under a per-number
lock), 5/h per MAC *on the source it was claimed from* (`otp.DeviceKey`) and 60/h per
address (in process, on `internal/ratelimit.Window`). The MAC — placed on the network
by the controller before any send — is the per-guest key; the address limit is a
bound on one host spraying numbers, sized so a NATed venue is not capped at twenty
guests an hour (the `otp` package comment has the survey). Codes are stored as an
HMAC under a KEK-derived key — never argon2, which a six-digit space defeats whatever
its cost — and presence is verified **before** a message is paid for.
[`internal/identity/CONTRACT.md`](../../internal/identity/CONTRACT.md) invariants 6–11.

**What "hard global spend cap" means here.** *Global* is read as it is meant — **a
bound on the total that nothing the attacker controls can raise**: not a number, not a
MAC, not an address. Foyer meets it with the per-site ceiling, which **has no default
and cannot be switched off** (`phone_sign_in.daily_limit NOT NULL`,
`updatePhoneSignIn` refuses a site without one, and phone sign-in cannot be offered at
a site that has none) and which fails closed. Sites are created only by an
administrator, so the deployment's total is bounded by the ceilings the operator
chose. The deployment-wide cap (`phone_spend_cap`, one row per tenant — and v1 ships
one tenant per deployment, so per tenant *is* per deployment) is a second, tighter
bound an operator may add. The field was surveyed: AWS SNS (account-wide monthly SMS
spend limit, USD 1 by default) and Firebase Authentication (per-project daily SMS
quota) make the account-wide cap mandatory, because an account there has unbounded
senders; Twilio Verify relies on Fraud Guard and geographic permissions instead.
Foyer's senders are bounded (a site each, each with its own mandatory ceiling), and
**there is no default deployment cap that would be right**: one below the sum of the
sites' ceilings silently overrides numbers the operator set per site, and one above it
bounds nothing — the same reason the per-site ceiling has no default. So the optional
cap stays optional; a change that makes a site able to send without its own ceiling
makes the deployment-wide cap mandatory in the same change.

**Each of these is a console setting within bounds, and none can be switched off:** a
deployment-wide daily cap sits above the per-site ceiling, counted in the same
transaction (`phone_spend_cap`, `phone_code_sends_total`); a venue may *narrow* the
allowlist to some of the Vietnamese mobile ranges and never widen it; every limit is
tunable only inside the bounds the contract and migration 00033's CHECKs state (the
per-address one is the one a NATed or cloud-hosted portal needs raised). The day every
ceiling counts is the site's own time zone, not the container's.

## 10. An irreversible change is audited fail-closed

Most handlers audit best-effort (`Server.record`): the change already happened, and
failing the response would lie about it. **An operation that cannot be undone is the
opposite case.** `eraseDataSubject` and `updateRetentionPolicy` write their entry in
the change's own transaction (`auditEntry` + the port's `record` argument), and an
export writes its entry before a byte leaves; if the entry cannot be written the
change is not made. A new irreversible operation takes one of those two shapes. An
erasure also rewrites earlier entries about the subject to a keyed pseudonym rather
than leaving the identifier in the log ([`database.md`](database.md), migration 00052).
