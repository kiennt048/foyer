# internal/nas — CONTRACT

## Invariants

1. **VerifyPresence is Mandatory**: `NASDriver.VerifyPresence()` MUST be invoked and return true before any captive portal grant is issued. Never grant access on unverified NAS redirect query parameters.
2. **Narrow Capability Gating**: A capability describes what the *driver* can do on the operator's behalf, not what the product may offer. Hiding a control is reserved for impossible actions; manual alternatives remain available.
3. **Controller/Client/AP Separation**: Controllers manage credentials and API endpoints; RADIUS clients define network IPs and secrets; Access Points map MACs discovered from controllers.
4. **Presence Verification on Controller Path**: UniFi verifies client presence via `GET {integration}/v1/sites/{siteId}/clients?filter=macAddress.eq('…')`.
5. **No Static Shared Secrets**: Generated shared secrets for APs/controllers must be cryptographically random and envelope-encrypted at rest.

## What VerifyPresence answers, and what it does not

`security.md` §4 asks for four things: the MAC is really associated, **on the
claimed AP**, **in the claimed site**, and **still unauthorized**.

The UniFi client lookup answers three of them in one call — the row existing
proves association, the site is the one in the URL, and `access.authorized`
proves it has not already been let in.

**The fourth costs a second call, and this driver pays it.** Nothing in the client
object identifies the access point by MAC, but the client object (MEASURED)
carries `uplinkDeviceId`, a UUID, and `GET /v1/sites/{siteId}/devices` resolves it
to that device's MAC — which is what the redirect's `ap` parameter carries. The
two identifiers are different kinds of thing, so the device list is the only
thing that joins them, and code comparing them directly finds nothing.

Three outcomes, and the split is the contract:

- **resolved and equal** → `PresenceVerified` with `Presence.APConfirmed == true`.
- **resolved and different** → `PresenceAPMismatch`, refused, **whatever
  `RequireAPMatch` says**. A configuration switch that could turn this off would
  be an unchecked access-point claim with a checkbox on it.
- **not resolved** — no `uplinkDeviceId`, or an id the site has no device for →
  `PresenceVerified` with `APConfirmed == false`. `UniFiConfig.RequireAPMatch`
  turns that residue into `PresenceAPMismatch` for a venue that scopes policy by
  access point. Default off, because refusing an unchecked claim refuses guests on
  hardware nobody has measured.

**The device list failing is an outage, not an unchecked claim.** It answers
`PresenceUnavailable` with the error attached, never a pass with `APConfirmed`
false. Fail-closed costs nothing on this family: the grant is a call to the same
controller over the same credential, so a controller that cannot answer the device
list cannot honour `AUTHORIZE_GUEST_ACCESS` either. The guest fail-open
`security.md` §2 permits is about the Policy API, where RADIUS can still let
somebody on without Go; here nothing can.

**The map is cached per site with a bounded TTL** — `unifiDeviceMapTTL`, five
minutes, named rather than inlined — so the steady state is one device call per
site per TTL rather than one per grant. A miss re-reads once, so an access point
adopted between two calls resolves instead of reading as absent.

**A second credential is not required for any of this.** The legacy device list's `vap_table` remains the only source of the BSSID →
access point mapping — MEASURED: the Integration API carries no BSSID in either
the list or the detail form — but nothing in this driver needs one, because the
redirect names the access point by device MAC. Nothing here may treat the absence
of a local admin account as an error or as a reduced capability;
`TestUniFiLegacyCredentialsAreOptionalAndCostNothingWhenAbsent` is the lock.

**The bool truncates a five-valued answer.** `PresenceDetailer` returns the
outcome, and callers that can act on it should: *not on this network*, *already
authorized* and *the controller is unreachable* are three different conversations
with a guest standing in front of you, and only the first two are refusals at all.
An unreachable controller returns `PresenceUnavailable` **and** a non-nil error —
never a bare false, which would read as a refusal.

## Evidence: a capability value and how it is known are separate facts

`Capabilities` has no third state, so an unmeasured value still ships as `true`
or `false`. `EvidencedDriver.CapabilityEvidence()` says which of MEASURED,
DOCUMENTED and UNVERIFIED each one is, using the same three words
`docs/protocol-reality.md` uses.

Two rules follow:

- **An UNVERIFIED value ships conservative.** False costs the admin console a
  control; true would promise behaviour nobody has seen happen.
- **An UNVERIFIED fact must name the measurement that would settle it.** A label
  with no experiment behind it is a guess in a costume, and
  `TestUniFiCapabilityEvidence` fails on an empty note.

**`liveReauthorize` follows the path the driver takes.** It was measured true on
the **Integration API** (`connectedAt` unchanged across two
`AUTHORIZE_GUEST_ACCESS` calls), so a driver
holding an API key reports it true, MEASURED, and implements `Reauthorizer` as a
second authorize. A driver holding **only a local admin account** drives
`cmd/stamgr`, which nobody has re-issued on a live client: it reports false,
UNVERIFIED, and its `Reauthorize` refuses with `UnsupportedActionError`.
`TestUniFiLiveReauthorizeIsWhatWasMeasuredAndNoMore` holds both sides. UniFi's two
remaining unmeasured capabilities are `walledGarden` and `rotateSecret`.

**Local-admin mode is a whole path, not an authorize branch.** With
no API key and a local admin account (`legacyMode`), presence is
`GET {network}/api/s/{site}/stat/sta/{mac}` — the station's `authorized` flag and
its `ap_mac`, compared directly against the redirect's claim — and the probe logs
in and reads `/api/self/sites` instead of reporting the Integration API's 401 about
a key that was never sent. A `VerifyPresence` that only ever called the
Integration API would refuse every guest on a local-admin controller before the
legacy authorize was reached. Community-documented; UNVERIFIED on real hardware. `TestUniFiLocalAdminModeCanVerifyAndAuthorizeAGuest` is the lock.

## The two evidence maps disagree, and a caller must not flatten them

The UNVERIFIED label has to reach the admin console rather than living here.

`EvidenceFor(vendor)` returns `CapabilityEvidence` and `AttributeEvidence`
**together**, and the reason is the disagreement between them:

| | |
|---|---|
| every **capability** of the CoA family | `UNVERIFIED`, without exception |
| the **standard** attributes | `DOCUMENTED` — RFC 2865/2866/2869 |
| the **vendor redirect** attributes | `UNVERIFIED` — a reading of a vendor table no device here has answered |

**The table is deliberately not summarised as** *every wire encoding is
DOCUMENTED*: `Session-Timeout` is defined by an RFC and `Cisco-AVPair`'s command
strings are not, and a screen that marked them alike would be telling an operator that
RADIUS itself is a guess — or that a vendor command string is as well established
as a standard.

A caller that merged the two lists into one badge would be answering *is this
driver trustworthy*, which is not a question either list asks. The sentence for a
Cisco row is **we know what we will send and we do not know what it will do**.

### `EvidenceLower` is a function, not a cast

This package spells its evidence values `MEASURED` / `DOCUMENTED` / `UNVERIFIED`
because `docs/protocol-reality.md` shouts them and a source file should too. The
contract's enum is lowercase, as every enum in that document is.

`gen.EvidenceStanding(fact.Evidence)` **compiles**, produces `"UNVERIFIED"`, and
is not a member of the enum — so a cast would answer with a value no client has a
branch for, and nothing in the Go server calls the generated `Valid()` on a
response on its way out. `EvidenceLower` converts, and a test holds it.

### `VendorForDriver` lives here because it is a rule

Two packages read it — the adapter and the in-memory fake — and a `switch` retyped
in each would be the *stale second copy* of a rule that `docs/rules/testing.md`
warns against.

`false` is the one answer that must never be defaulted: a `coa` row that names no
vendor. Cisco and Aruba identify a session differently and a wrong guess is **an
ACK with no effect** — the one failure a fake NAS cannot produce, because a device
that ignores what it cannot match answers success.

## The fake claims no more than the driver it stands in for

`MockDriver.Caps` is **derived, not written**: `ShippedCapabilities(vendor)` reads
the built driver for that vendor, and a vendor Foyer ships no driver for gets the
floor rather than a guess. `ShippedCapabilityEvidence` does the same for how those
values are known, so `ProbeResult.Unverified` names the two UniFi cannot back (walled garden and
secret rotation).

A literal would drift towards the most capable driver that could exist —
`WalledGardenFQDN` where the shipped driver reports `WalledGardenNone`,
`RotateSecret: true` where every vendor in the design reports false. A capability
decides whether an admin control **exists at all**, so a test reaching a gate
through such a fake would reach a branch no deployment takes.

Two rules hold here:

- **A test that needs a capability the shipped driver lacks asks for it**, through
  `WithCapabilities`, where the assumption is a written sentence. The asymmetry is
  the point: assuming a capability is absent fails loudly and gets fixed, assuming
  one is present passes forever against a product nobody ships.
- **The fake is no more agreeable than the driver either.** `AuthorizeGuest`
  refuses a MAC it does not hold, because UniFi's resolves the client on the
  controller first and answers `no client %s in site %s` when it cannot.

## The fake can fail, and can produce every outcome the type declares

A stand-in more agreeable than the thing it stands in for leaves the product's
branches for a refusal, an outage or a failed revocation with no route into them.
So `VerifyPresenceDetail` produces every `PresenceOutcome` — including
`already-authorized`, `site-mismatch`, `ap-mismatch` and `unavailable` — and
`DeauthorizeGuest`, `DiscoverAccessPoints` and `Probe` can each fail.

`MockClient` is what the mock's controller knows — site, access point, whether it
has already been let in — and every field is checked **only when it carries
something to check against**, which is `Presence.APConfirmed`'s own rule: an
answer that carried nothing to compare is an unchecked claim and is reported as
one, never a quiet pass. `MockFaults` splits the failures the way the product
does: `Unreachable` is a property of the controller and fails everything at once,
because a controller nobody can reach does not answer presence and refuse a
revocation. The per-call fields are a controller that is up and refusing one
thing, and `MockAPIRefusal` produces the `*APIError` shape a driver matches with
`errors.As` before deciding a CoA fallback is justified. **Both sides of that
branch are drivable from the shipped fake.**

Three rules hold here:

- **The additions are opt-in and the defaults are untouched.** `NewMockDriver`
  still answers present for any unlisted MAC; `NewStrictMockDriver` still does
  not. A test that wants an outcome asks for it in one line, which is where the
  assumption is visible.
- **The wording is the fake's own, never the driver's.** `ErrMockControllerUnreachable`
  does not reproduce UniFi's message string. A fake that copied it would be a
  second copy of a fact one file away — the stale-second-copy mistake
  `docs/rules/testing.md` names — so what a caller may rely on is the *shape*:
  a non-nil error beside `PresenceUnavailable`.
- **The lock is derived, not listed.** `TestMockDriverProducesEveryPresenceOutcome`
  compares what the fake produced against every value the type declares, so a
  seventh outcome fails the test rather than passing it.

Tests use the shipped mock rather than hand-rolled fakes. Three of its features
exist for that: `AuthorizeLog` / `AuthorizeCount` (the
`Authorized` map is keyed by MAC, so two authorizations of one device count as
one — and *asked twice for one MAC* is exactly what a double submit produces),
`WithHooks` (a function that runs **inside** a call, which is what proves the 303
is written *before* the NAS is asked — Android destroys the WebView the instant
the network validates, and a fake that only counts cannot see the ordering), and
`WithDefaultPresent` (a guarded flip while a server is serving, for the sabotage
a refusal test carries inside itself). The type has a mutex for the last one:
`httptest` handler goroutines outlive the response, and a bare field write there
is a data race, not a style question.

## A probe warning is a kind, and the driver's set is wider than the API's

`ProbeResult.Warnings` is `[]ProbeWarning` — `Kind`, `Params`, `Detail` — never
`[]string`. The contract's `ProbeWarning` is a kind plus params, so filling it from
an English sentence would mean matching on prose, which is the hand-maintained copy
`docs/rules/database.md` warns against, with a regular expression on top.

- **`Detail` is for a human reading a Go value** — `foyerctl probe-unifi`, a log
  line, a support bundle. The admin API sends the kind and the console owns the
  copy. **Every figure travels in `Params`**, never formatted into `Detail`: the
  moment a number exists only inside a sentence, the only way to get it back is
  to parse the sentence.
- **`WarningKind` is deliberately wider than `gen.ProbeWarningKind`.**
  `WarnCertificateVerificationDisabled` has no contract kind, on the contract's
  own stated grounds that its `TlsMode` cannot produce the condition. `foyerctl`
  and `lab/` *can* build such a driver, so the driver reports it and the boundary
  drops it — `apistore.warningsOut`, which drops any kind the contract does not
  name rather than coercing it into one the console has copy for.
- **`WarnAccessPointUnconfirmed` is unconditional on the CoA family and
  conditional on UniFi**, and the asymmetry is the point. The UniFi driver
  *checks* the access point, so a notice on every probe would say the opposite of
  what the code does — and a warning that appears on every healthy probe is one an
  operator learns to skip. `UniFiDriver.Probe` raises it only when this controller
  cannot answer the question: no API key, a device list that does not answer, or a
  site with no devices in it.
- **`ShippedProbeWarnings(vendor)` is the single source of a driver's
  *unconditional* caveats**, read off the built driver exactly as
  `ShippedCapabilities` is. It answers nil for UniFi, and nil is a result rather
  than an omission — `ShippedConfirmsAccessPoint(vendor)` is the half that makes
  it readable, because an absent §4 notice means *this driver checks* on
  one vendor and *somebody forgot* on another. `TestShippedDriversRegisterThemselves`
  weighs the two against each other in both directions.
- **`MockDriver` derives its §4 notice from its own world**, not from the shipped
  list. A fake standing in for UniFi that copied the (empty) list would report
  a controller with no caveats while its own `VerifyPresenceDetail` answered
  `APConfirmed: false` for every client — the probe and the presence answer
  describing two different controllers. It warns when it holds no `MockClient`
  carrying an `APMAC`, which is the same field `APConfirmed` is derived from.

## VendorProfile — the second abstraction, and everything in it is UNVERIFIED

`docs/rules/nas-drivers.md` opens by saying vendor families differ in **how a
client is made captive in the first place, and that happens inside the RADIUS
reply — not out of band**. `VendorProfile` is that seam, so `internal/aaa` renders
no reply attributes itself: `RenderReply`, `Unrenderable`, `OperatorPrerequisites`,
`AttributeEvidence`.

**No attribute in any profile has been verified against a device.** No Cisco,
Aruba, Ruckus or MikroTik hardware has been measured. The attribute *names* are
DOCUMENTED — the RFCs for the standard set, the vendor table in the rules file
for the redirects — and that any NAS **honours** what is rendered is UNVERIFIED
without exception. `AttributeEvidence` says so per attribute and names the
measurement that would settle it, and `TestNoVendorAttributeClaimsToBeMeasured`
fails on a vendor attribute that claims anything else or names no measurement.

Five things in this seam are load-bearing:

- **`RenderReply` returns a slice, not a map.** Cisco's captive-portal reply is
  **two `Cisco-AVPair` attributes** — `url-redirect` and `url-redirect-acl` — and
  a `map[string]string` holds one. `CollapseToMap` folds a reply into the
  single-valued dictionary `rlm_rest` is answered with today **and names what it
  had to drop**. The ACL is rendered first so the survivor is the half whose
  absence is dangerous: without it the client is not made captive at all.
- **VLAN attributes are emitted untagged.** RFC 3580 describes tagged
  attributes and FreeRADIUS spells a tag with a colon — and a colon in a key is
  refused by `aaa.ForbiddenReplyAttribute` **by shape**, which drops the *whole*
  reply as a 503 with a credential-leak alarm beside it. A tagged name here would
  take out every dynamic-VLAN reply in the product.
- **`Unrenderable` is a fact about the profile, never a claim about the vendor.**
  "This profile cannot say it" and "the platform cannot do it" are different
  statements and only the first is knowable from here.
- **`OperatorPrerequisites` is where *"the operator configures Foyer, not
  FreeRADIUS" is true; "the operator configures nothing" is not* becomes a value
  a screen can render.** Cisco's `url-redirect-acl` names an ACL that must
  already exist on the NAS; Aruba's role is the same shape; and so is `Filter-Id`.
- **`UniFiProfile` renders no redirect, on purpose.** UniFi's captive-portal
  *authorization* has no RADIUS equivalent, so a profile that exists and renders
  nothing is a different statement from no profile at all — and the two are
  indistinguishable from `VendorProfileFor` unless one of them is a file.

**Registration happens from each profile's own `init()`.** The core has no table
to append to, so adding a profile is one file with no core change, and
`TestAddingAVendorProfileNeedsNoCoreChange` proves it by adding a fictional
vendor from a test file and rendering through it.

Ruckus has a profile and must never get a `CoADriver`: its captive-portal model
is the Northbound JSON API, and RADIUS Disconnect fails there in non-proxy
MAC-auth mode. That is a statement about `NASDriver`, not about the profile, and
it is the separation these two abstractions exist for.

## UniFi: flavour is probed, never asked

Two deployment shapes, differing in three ways an operator cannot be expected to
know. `GET /proxy/network/status` answering **200 or 401** means UniFi OS; a 401
is the positive signal.

| | UniFi OS | Standalone |
|---|---|---|
| Integration API | `/proxy/network/integration/v1/…` | `/integration/v1/…` — **UNVERIFIED**, derived, and the probe reports what actually answered |
| Legacy private API | `/proxy/network/api/s/{site}/…` | `/api/s/{site}/…` |
| Login | `/api/auth/login` | `/api/login` |
| `cmd/stamgr` | **403 unless the `X-CSRF-Token` from the login response is echoed** | no token needed |

The action endpoint takes the controller's **client id, never the MAC**; the id
comes from the lookup call that `VerifyPresence` already makes.

## TLS is a choice the operator makes explicitly

Controllers ship self-signed certificates. `TLSMode` is `verify` by default,
`pinned-sha256` for the on-prem case, and `insecure-skip-verify` only when
somebody typed it — and `Probe()` warns whenever it is in force. A driver whose
TLS configuration cannot be honoured is returned **inert**: every call fails with
the configuration error rather than reaching the controller unverified.

**A pin is compared against the leaf (`rawCerts[0]`) and nothing else.** With the
platform verifier off, the rest of the presented chain is a list the peer typed and
nothing checks that it signs the leaf; the controller's certificate is public, so a
match "anywhere in the chain" accepts `[attacker-leaf, real-controller-cert]` and
hands over the API key. The fingerprint recorded as *observed* is the leaf's for the
same reason. Pinned by `TestUniFiPinIsCheckedAgainstTheLeafOnly`, which covers `pinned-sha256` and the strict half of
`trust-on-first-use`.

## CoA is two things: UniFi's revocation fallback, and a driver family

> UniFi's captive-portal *authorization* has no RADIUS equivalent. Revocation
> does.

`CoAClient` (RFC 5176 Disconnect) is used by `UniFiDriver.DeauthorizeGuest`
only when the controller is **unreachable** — never when it answered and refused,
because disconnecting anyway would be acting on our own opinion of its answer.

The same client is the whole of `CoADriver`. **One encoder, not two**: a
second RFC 5176 implementation would be the hand-maintained-copy failure at
protocol scale.

Nothing in `coa.go` has met real hardware. `docs/rules/testing.md`: *"ACKing a
CoA is trivial; faithfully rejecting one is not."* The port is per-NAS (Cisco
frequently uses 1700), NAKs return a typed `*CoAError` carrying `Error-Cause`,
and silence is retransmitted a bounded number of times and then named
`ErrCoATimeout`.

### Vendor-Specific encoding, and what it does and does not prove

A `CoADriver` needs type-26 encoding to **reauthorize** — Cisco's is a CoA-Request
carrying `Cisco-AVPair = subscriber:command=reauthenticate`. `VendorAttribute`,
`VendorString` and `encodeVendorAttribute` provide it, and `CoAClient.CoA` is the
second request code.

Two facts of different standing live in the same bytes and must not be conflated:

- **The framing is DOCUMENTED and is proved.** RFC 2865 §5.26: type 26, four
  bytes of vendor id, then the recommended `vendor-type, vendor-length, value`
  sub-attribute. `TestVendorAttributeWireFormat` asserts the bytes against a
  **hand-derived** expectation rather than a round trip, because an encoder and a
  decoder written together agree about their shared mistakes; the two length
  fields are also asserted against each other, which is where an off-by-one lives.
  The fake NAS decodes with a parser written from the RFC, not from `coa.go`.
- **The numbers are DOCUMENTED**: `VENDOR Cisco 9` / `Cisco-AVPair 1`,
  `VENDOR Aruba 14823` / `Aruba-User-Role 1`, `VENDOR Mikrotik 14988` /
  `Mikrotik-Rate-Limit 8`, read out of `/usr/share/freeradius/dictionary.*`
  **inside the image `lab/` builds** — the file the server itself parses.

  **They are read from the dictionary because being wrong is undetectable from
  here**: a bad vendor id produces a syntactically valid attribute the device
  ignores, so the NAS answers **ACK** and does nothing — the failure
  `docs/rules/nas-drivers.md` names, arriving through the one channel a typed
  `Error-Cause` cannot see. No amount of correct NAK handling would surface it.

  **A dictionary settles spelling, never behaviour.** The AV-pair command
  strings, the MAC spelling each platform matches on, the rate pair's direction,
  and whether a NAS NAKs 503 rather than ignoring an unknown session are all
  still UNVERIFIED.

One VSA per RADIUS attribute, never several packed into one: RFC 2865 §5.26
permits packing and warns against it in the same paragraph, and packed
sub-attributes could not be told apart downstream.

Two client-side refusals go with it. A request naming **no session** is
refused before it is sent (`ErrCoANoSessionIdentity`) rather than depending on
whether the far end is careful enough to answer 402. And a reply is checked
against the **request code family**: a Disconnect-ACK is not an answer to a
CoA-Request, and reading it as one records that a guest's limits were raised at
the moment they were thrown off the network.

## CoADriver — what a driver can be when the protocol has no query in it

`NASDriver` was shaped around a controller: something you can ask. RFC 5176 is
one-way. Three consequences, and none is a detail:

- **`VerifyPresence` cannot be answered over CoA.** The evidence available
  instead is Foyer's own accounting — the RADIUS server saw this MAC authenticate
  from this NAS — which is a `SessionSource`, **declared here and implemented in
  `internal/store`**, exactly as `aaa.PolicyStore` is. With none configured the
  driver answers `PresenceUnavailable` **and an error**, never `true`. Of
  `security.md` §4's four checks it makes **one** (the MAC), plus one §4 does not
  list (the session is on *this* NAS, compared on NAS-IP-Address or
  NAS-Identifier and only where both sides carry one). AP, site and
  still-unauthorized are unavailable, `APConfirmed` is false on every answer, and
  `Probe` says so unconditionally.
- **`DiscoverAccessPoints` returns `ErrNoController`**, not an empty list. The
  device is its own controller — the contract agrees from the other end, where
  `ControllerVendor` is `[unifi, omada, ruckus]` and excludes this family. "There
  are none" and "there is nothing that could enumerate them" are different
  answers and only one is true.
- **`Probe` has to write.** It sends a Disconnect-Request for a session id that
  cannot exist (`foyer-probe-` plus 128 random bits) and reads the refusal. A NAK
  is the healthy answer — the device is there and the secret verified. An **ACK**
  is `WarnCoAAcknowledgedUnknownSession`: reachable and untrustworthy, because a
  device that ACKs whatever it is sent means no revocation through this driver can
  be trusted to have revoked anything. A reply that does not verify is
  `ErrCoABadAuthenticator` with `Reachable: true` — something answered, and the
  fault is the credential. Silence is reported as **ambiguous** and names all four
  causes including the wrong port.

`Reauthorize` is the fourth action `docs/rules/nas-drivers.md` names —
*"verify, authorize, revoke, reauthorize"* — and **`NASDriver` does not have it**.
It is an optional interface (`Reauthorizer`), following `PresenceDetailer` and
`EvidencedDriver`, so no existing caller changes. Without it,
`Capabilities.LiveReauthorize` would gate an admin control with no method to call.

### Three vendors, three different answers, each from a real constraint

| | Cisco | Aruba | MikroTik |
|---|---|---|---|
| `CanAuthorizeGuest` | **true** — `subscriber:command=reauthenticate` | false | false |
| `LiveReauthorize` | true | false | **true** |
| `BandwidthLimits` | false | false | **true** |
| Identifies on | audit-session-id → Acct-Session-Id → MAC | MAC → Acct-Session-Id | Acct-Session-Id → User-Name → MAC |

- **Aruba's false is a finding about Foyer, not about Aruba.** On AOS the
  authorization *is* the user role, and `AuthorizeRequest` carries UniFi's four
  limits and **no role field at all**. A CoA-Request with only identification in
  it asks the controller to change nothing. Widening `AuthorizeRequest` is a
  decision for the grant path, not something to slip in from a vendor file —
  **stated, not worked around.**
- **MikroTik's pair is the narrow truth**, not an inconsistency: RFC 5176 changes
  a session and cannot create one, so a hotspot client that has not logged in has
  nothing to change; but a live session's rate *can* be changed, and
  `AuthorizeRequest` does carry rates.
- **`Mikrotik-Rate-Limit` is rendered by one function** for both halves, because
  `MikroTikProfile` puts it in an Access-Accept and `MikroTikCoAVendor` puts it in
  a CoA-Request and they are the same attribute on the same device. Two copies
  would drift *asymmetrically*: one rate at login and a different one the moment
  an operator pressed apply.
- **The MAC spelling is the largest guess**; the vendor numbers are DOCUMENTED. Calling-Station-Id has no canonical form and
  every vendor differs — and unlike the numbers, no dictionary carries it.
  `TestMACSpellingDecidesWhetherASessionIsFound` makes it mechanical: the fake
  stores one spelling and matches on string equality, so a vendor rendering the
  other gets 503. That is the *visible* half of being wrong; the invisible half
  is a device that ignores what it cannot match, and no harness here produces it.

### Two evidence maps, because one label cannot carry two standings

`CoAVendor` has `CapabilityEvidence()` **and** `AttributeEvidence()`, and the
split is load-bearing rather than tidy. *"Cisco-AVPair is vendor 9, attribute 1"*
is DOCUMENTED; *"a Cisco NAS honours `subscriber:command=reauthenticate`"* is
UNVERIFIED and no dictionary can settle it. One `CapabilityFact` carries one
label, so a single map would round both to whichever is louder — and the loud one
is the wrong one, because an encoding that is right buys nothing if the device
ignores the value inside it.

Two tests hold them apart and must disagree about the same vendors:

- `TestNoCoAVendorClaimsToBeMeasured` — **every capability stays UNVERIFIED**, and
  names the measurement that would settle it.
- `TestCoAWireNumbersAreDocumented` — every emitted attribute's encoding is
  DOCUMENTED, **cites the dictionary**, and *still names what remains unmeasured*,
  so a promotion cannot delete the caveats along with the label. It also
  re-checks the capability map, so promoting a capability on the strength of a
  dictionary read fails **both** tests.

`Aruba-User-Role` has a DOCUMENTED encoding that **nothing emits**, and that is
deliberate: the reason it is unused is a missing `Role` field on
`AuthorizeRequest`, not a missing measurement. Recording it separates *"nobody has
worked out how"* from *"we know how and the type cannot carry it"*, and only the
second is actionable.

**Behaviour is derived from the capability, not written beside it.**
`AuthorizeGuest` refuses when `CanAuthorizeGuest` is false and `Reauthorize`
refuses when `LiveReauthorize` is false, and `TestCoACapabilitiesMatchBehaviour`
asserts both directions plus `CanAuthorizeGuest ⇒ LiveReauthorize`. A capability
saying yes over a method that refuses is a control that silently does nothing; one
saying no over a method that works hides a feature for nothing.

### Ruckus has a profile, has no driver, and must never get a CoA one

`docs/rules/nas-drivers.md`: *"Ruckus is not a CoA vendor for captive portal."*
The Northbound JSON API is a surface nothing in this repository documents — no
endpoint, no authentication shape, no error model — so **there is no
`RuckusDriver`**: writing one without that would be invention. `TestRuckusIsNotInTheCoAFamily`
guards the tempting shortcut (*"it is RFC 5176, point CoADriver at it"*), which
produces a driver that reports success and disconnects nobody.

### The fake NAS refuses eleven ways and accepts one

`coa_nas_test.go`, written against what `docs/rules/testing.md` says a lab cannot
fake easily — faithful refusal:
four silent drops (short packet, missing Message-Authenticator, either
authenticator failing to verify, an undecodable attribute) and seven NAKs (402
Missing-Attribute twice — no identification, and a CoA that changes nothing; 503;
508; 405 for a device that implements Disconnect and not CoA; 401 for an unknown
vendor attribute; 504 for a session it can see and will not end).
`TestFakeNASRefusesMoreThanItAccepts` drives every one, because a fake whose
refusal branches have never executed is a fake that only ACKs wearing a comment.

**What it still cannot do, stated rather than implied:** it is not picky about
source IP (the client dials a connected socket, so there is one source), never
answers slowly, never reorders or fragments, and never disagrees with itself
between two requests. Real gear does all of that.

## Adding a driver

`ShippedCapabilities`, `ShippedCapabilityEvidence` and `ShippedProbeWarnings` read
from a registry, not from a `switch vendor` in `driver.go`: each driver registers
itself with `RegisterShippedDriver` from its own file's `init()`, the same shape
`RegisterVendorProfile` uses for profiles. There is no table in the core to append
to. That matters beyond tidiness: `MockDriver` derives its capabilities from those
functions, so a `switch` with a `default:` arm would let a vendor's fake claim the
floor while its real driver claimed otherwise.

`TestAddingACoAVendorNeedsNoCoreChange` executes the claim: a fictional vendor
defined entirely inside a test file, driving a real NAS conversation through the
shared encoder, registering nothing in `driver.go`. Making a vendor selectable by an
operator additionally needs its `Vendor` constant, a `VendorForDriver` mapping and
the contract's driver enum ([`docs/rules/nas-drivers.md`](../../docs/rules/nas-drivers.md)).

**One limit, stated.** `unconditionalWarnings` is unexported, so a driver defined
*outside* `internal/nas` cannot supply probe warnings to `ShippedProbeWarnings` and
gets nil. Every driver lives in this package, so this is a language fact rather
than a gap — exporting the method would put it on `NASDriver` for the sake of a
value only this package's own stand-in reads.

`WarningKind` is the core's vocabulary: a driver observing a condition none of the
kinds names has to add one (as the CoA family adds
`coa-acknowledged-unknown-session`). The boundary tolerates it —
`apistore.warningsOut` drops any kind the contract does not name rather than
coercing it — so a new kind needs no contract change until the console has copy
for it.
