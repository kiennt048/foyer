# NAS driver rules

> Governs `internal/nas` — `NASDriver`, `VendorProfile` and the per-vendor drivers.

## Two abstractions, because there are two variable surfaces

Vendor families differ in *how you let a client in*, and also in **how a client is
made captive in the first place** — which happens inside the RADIUS reply, not out of
band. Hence:

- **`VendorProfile`** — renders an abstract decision into vendor attributes.
  Consulted by the Policy API while answering an Access-Request.
- **`NASDriver`** — performs out-of-band actions: verify, authorize, revoke,
  reauthorize.

Do not collapse them. The redirect attributes are **not** a standard:

| Vendor | Captive-portal attributes |
|---|---|
| Cisco | `url-redirect`, `url-redirect-acl` — **Cisco AV-pairs, not a standard** |
| Aruba | `Aruba-User-Role`, `Aruba-Captive-Portal-URL` (AOS 8.4+) |
| Ruckus | `Ruckus-External-Url` |

## Enforcement families

| Driver | Mechanism | Status |
|---|---|---|
| `UniFiDriver` | Controller API (`cmd/stamgr` → `authorize-guest`) | shipped |
| `CoADriver` (MikroTik, Cisco, Aruba) | RFC 5176 CoA | shipped |
| Ruckus | **Northbound Interface JSON API** — *not* CoA | planned; a `VendorProfile` exists, no driver |
| Omada | Controller API | planned |

**Ruckus is not a CoA vendor for captive portal.** SmartZone's external-portal
model is the Northbound JSON API. PacketFence's own documentation says that for
MAC-auth in non-proxy mode you must set the deauthentication method to HTTP/HTTPS
so the Northbound API is used *instead of* RADIUS Disconnect — otherwise "the
code will try to use RADIUS by default and fail to disconnect the user."

**UniFi *can* do RADIUS CoA** (Ubiquiti documents port 3799). The precise claim is
narrower than "UniFi has no CoA": **UniFi's captive-portal *authorization* has no
RADIUS equivalent.** Revocation does — so `Revoke()` may use CoA-Disconnect as a
fallback when the controller API is unavailable, which usefully de-risks vendor
API drift.

## Session identity differs per platform — carry all of it

```go
type Session struct {
    AcctSessionID    string // Cisco fallback
    CallingStationID string // MAC — Aruba, UniFi
    AuditSessionID   string // Cisco IOS-XE: Cisco-AVPair audit-session-id
    NASIPAddress     netip.Addr
    NASIdentifier    string
    SiteID           string // UniFi
}
```

Never assume one field identifies a session everywhere.

## CoA specifics that bite

- **The port is per-NAS, not a constant.** 3799 is common; **Cisco frequently
  uses 1700**. Make it a field.
- CoA is **UDP** — implement retransmit and a deterministic timeout.
- Handle `CoA-NAK` with `Error-Cause` **503 Session Context Not Found** and **508
  Multiple Session Selection Unsupported**. A driver that ignores these reports
  success on a no-op.
- Return a **typed error carrying Error-Cause**, never a bare `error`.

## Capabilities are load-bearing, not documentation

```go
type Capabilities struct {
    LiveReauthorize bool       // UniFi: MEASURED true on the Integration API; UNVERIFIED on a local admin account
    WalledGarden    GardenKind // none | ip-range | fqdn-sni
    HardQuota       bool       // NAS enforces quota itself → prefer it
    BandwidthLimits bool
    DynamicVLAN     bool
    RotateSecret    bool       // PUSH a secret to the device — generating one is Foyer's
}
```

- **Callers must check before relying**, and read the capability's own definition
  before acting on it: it says what the *driver* can do on the operator's behalf, not
  what the product may offer. A false one may make an action **impossible** — hide the
  control — or merely **manual**, in which case keep it and state the price. Either
  way, never show a field that silently does nothing.
- **Prefer the narrower reading.** `RotateSecret` is the manual kind: gating
  **Generate new secret** on it would leave a site whose driver cannot push with no way
  to rotate a secret at all. *UniFi cannot do CoA* is the same shape — see above. Both
  claims are true only in their narrow form.
- `WalledGarden` is three-valued on purpose. An IP-range-only garden is
  materially weaker than FQDN/SNI and must be documented as such.
- `HardQuota` changes who enforces quota — see [`radius.md`](radius.md).

## VerifyPresence is mandatory

Before any grant is created. NAS redirect parameters are attacker-controlled —
UniFi's are unsigned query strings. See [`security.md`](security.md) §4. This is a
**driver obligation**, not something the portal can approximate.

## What Foyer cannot configure

`url-redirect-acl` names an ACL that must **already exist on the Cisco NAS**.
Foyer cannot create it. "The operator configures Foyer, not FreeRADIUS" is true;
"the operator configures nothing" is not. Ship per-vendor NAS configuration as
**tested artifacts in `lab/`**, not as prose in `docs/`.

## Adding a driver

Inside `internal/nas` there is no table to append to: a driver registers itself with
`RegisterShippedDriver` and a profile with `RegisterVendorProfile`, each from its own
file's `init()`. `TestAddingACoAVendorNeedsNoCoreChange` and
`TestAddingAVendorProfileNeedsNoCoreChange` hold that, by defining a fictional vendor
entirely in a test file. Making a new vendor selectable by an operator also needs its
`Vendor` constant, a `VendorForDriver` mapping and the contract's driver enum.

**If you find yourself changing anything else in the core to add one, that is a
finding about the abstraction — report it in an issue rather than working around
it.**
