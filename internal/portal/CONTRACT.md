# internal/portal — CONTRACT

The guest portal: what a device may be told, what it may be granted, and the
browser-to-MAC binding that connects the two.

The contract operations tagged `portal` (`getPortalContext`, `redeemVoucher`,
`acceptTerms`, `loginWithAccount`, `requestPhoneCode`, `verifyPhoneCode`,
`getPortalStatus`, `withdrawOwnMarketingConsent`) are handlers in `internal/api`, because
that is where the generated `StrictServerInterface` lives. What they **decide** is
here.

## Invariants

1. **`VerifyPresence` before any grant, with no configuration that skips it.**
   NAS redirect parameters are attacker-controlled — UniFi's `ap`, `id`, `t`,
   `url` and `ssid` are unsigned query strings anyone within radio range can
   supply. Without the check the portal is a public API for authorizing arbitrary
   MACs, including devices that never associated
   (`docs/rules/security.md` §4).

   **`NewService` refuses to construct without a `Drivers` port.** The tolerant
   version of this check — *if the driver is nil, skip it* — would mean a process
   that failed to build a driver answers `303 authorized` to any MAC in a request.

2. **The bool is the gate; the outcome is what the guest is told.** Three
   different situations produce `false` and they are not one sentence:

   | Verdict | Driver outcome | Grant? | The guest is told |
   |---|---|---|---|
   | `VerdictProceed` | `verified` | yes | — |
   | `VerdictAlreadyOn` | `already-authorized` | **no** | sent to the connected page |
   | `VerdictRefuse` | `not-found`, `site-mismatch` | no | `not-present` |
   | `VerdictRefuse` | `ap-mismatch` | no | `ap-unconfirmed` |
   | `VerdictUnavailable` | `unavailable`, **or any non-nil error** | no | `generic` |

   `already-authorized` claims no voucher: burning a single-use code for a guest
   who is already online is a support call that ends with staff issuing a second
   one. `unavailable` is the venue's outage and must never read as a statement
   about the guest's phone — *this device is not on the network* sends somebody to
   toggle Wi-Fi for twenty minutes, and *something went wrong, try again* sends
   them to the counter, where a person can see the controller is down.

   **One verdict, two sentences, and that is why the column is not derived from
   the first one.** A `Reason()` method on `Verdict` would make what the guest reads a function of what the portal *does* — and a refusal
   is one action over more than one situation, so the sentence would have to be
   the widest that fits all of them. `Explain(nas.Presence, error)` reads the
   outcome; there is no `Verdict.Reason()`, because one beside `Explain` would be a
   second, cruder copy of the same rule.

   **`ap-mismatch` is the guest who is on this network** — the controller
   returned a client for that MAC and then declined to vouch for the access point
   the redirect named — so `not-present` was false of them in both halves: they
   are here, and *turn Wi-Fi off and on* is a loop for at least one of the two
   situations `internal/nas` collapses into that outcome. **`site-mismatch` keeps
   `not-present` deliberately:** access points inside a site are one network, so
   the sentence is false for a device under the wrong one; sites are separate
   networks, so it is true as written.

   **The error is read before the outcome**, in `Classify` and in `Explain` —
   which asks `Classify` rather than re-testing it, so the two cannot drift into
   disagreeing about one answer. An `OK: true` beside a non-nil error is a driver
   bug, and trusting the `OK` is how a driver bug becomes a grant.

3. **The session is bound to one MAC and the binding is never re-pointed.** A
   request carrying a session for one address and NAS parameters naming another
   does not update the row: the old session is forgotten and a new one is minted,
   which then has to pass `VerifyPresence` on its own.

   **The narrow claim is the true one.** Nothing stops somebody deleting their own
   cookie and starting again, so this is not a barrier to *trying* a second MAC.
   What it prevents is a grant issued after a presence check on one address being
   carried over to another.

4. **The authenticating operations answer `303 See Other` on every
   outcome**, and the NAS is asked to authorize **after** the response is written.
   The CNA re-checks captivity only after a document navigation; Android destroys
   the login WebView the instant the network validates. `Outcome.Authorize` is a
   closure the handler runs from inside the response writer, so the ordering is a
   property of the type rather than of somebody remembering.

5. **No state survives the portal.** The CNA's `WKWebsiteDataStore` is ephemeral:
   cookies, `localStorage`, IndexedDB and service workers are memory-only and
   destroyed on close. Everything lives server-side; the client carries one opaque
   token whose only job is to name a row. The cookie sets no `Max-Age` — a
   persistence attribute would be a promise to a client that cannot keep it — and
   `Secure` follows the connection, because a captive portal is regularly reached
   over plain HTTP and setting it unconditionally would produce a portal that
   works in a browser and silently drops every cookie on a phone.

6. **Language is an allowlist, not an escape.** The submitted `lang` lands in a
   `Location` header. Two known strings cannot carry a traversal, a newline or a
   scheme; an escape can always be got wrong by whoever adds a third language.

7. **Nothing submitted is echoed, except a voucher code, and that only to the
   voucher page.** The code comes back because a guest cannot reasonably retype
   twelve random characters one-handed, and it comes back **normalised** so the
   value the portal renders into a field is `[A-Z0-9]` by construction. A username
   is not in that position and a password never is: `Failure` gates the echo on
   the page, so a caller passing a username gets it dropped rather than published.

8. **Open redirect: allowlisted on the way in and on the way out.** `http` and
   `https` only; no protocol-relative, no userinfo, no control characters, 2 048
   bytes. Rendered as a link the guest chooses to follow, never an automatic
   bounce. `PortalStatus.originalUrl` carries the same rule as a schema `pattern`,
   and a test requires the two to agree — a value this vouches for and the schema
   rejects is a `500` on the one screen a guest sees after typing a credential.

9. **Grant state is derived; revocation is stored.** Expiry is the clock passing a
   number and nothing has to happen for it to be true, so a stored `expired` is
   wrong for as long as it takes a timer to fire — and a timer that never fires
   leaves a guest reading *connected* beside a timestamp that ran out an hour ago.
   Revocation is an event and is written when it happens.

   **`Delivery` is the second stored one**, and it is stored for revocation's
   reason rather than expiry's: a call was made, it returned, and there is nothing
   about *now* from which that could be recomputed. Asking the controller again to
   find out would be a second authorization attempt disguised as a read.

10. **A failed authorization is recorded, and it is not an audit entry.**
    Invariant 4 puts the driver call after the response, so its failure can reach
    nobody through that response — a guest whose grant exists and whose controller
    call failed reads the connected page with no internet.

    It is **not** an `AuditKind` and adding one would be wrong. The audit log
    records what an **operator did**: `AuditEntry` requires `actorId` and
    `actorUsername`, and there is no operator here — a guest typed a voucher and a
    controller refused. Filing it there would need an actor invented, and
    `internal/api`'s invariant 10 forbids the near-enough kind that would come with
    it. The right surface is `listAuthAttempts`, which declares `AuthSource: driver` — *"the controller refusing
    after every check Foyer owns has passed, which is invisible from both of the
    other two sources"* — and `AuthEventKind: authorize-failed`.

    So the `Authorizations` port carries it, and **one method writes both readers**:
    the operator's event and the grant's `Delivery`. An implementation that could
    write one without the other would let the console show `authorize-failed` beside
    a portal showing a green tick, which is worse than silence. A
    SQL implementation does both in one transaction.

    A nil port gets a **log-only recorder**, not nothing — there is no safe version
    of *nothing* here — and it says on the way past that it reaches one reader out
    of three.

    **Retried, boundedly, and the retry is visible.** Three attempts, but only for a
    controller that did not answer, or one that answered `5xx`/`429`. A controller
    that answered `401` has an opinion and repeating the question repeats the
    answer; the narrow reading of *retry the transient failure* is the true one.
    `Authorization.Attempts` is recorded, because a retry nobody can see is a second
    way to be wrong: an operator reading *the network was opened* would have no way
    to tell a healthy controller from one that missed the first ask.

    The recording gets **its own context and budget**. The driver call runs under
    the caller's deadline, so a controller that hangs until that deadline would
    leave nothing for the write that says it hung — the one case where the record
    matters most is the one where it would silently not happen.

11. **The pre-authentication state is bounded three ways** (§5, "there is no
    identity yet, so nothing else bounds it"). The byte cap belongs to the walled
    garden and is a data-plane concern; the time limit is `Session.ExpiresAt`; the
    rate limit is the `Limiter` port.

    **What the rate limit is keyed on is the security property.** Every
    authenticating operation runs
    **source budget → `VerifyPresence` → device budget**:

    - `allowSource` spends a `KeySource` bucket keyed on `Session.Source` — the TCP
      peer of this request, set by `Resolve`/`Lookup` from `RemoteAddr`, never
      stored and never a header — through `ratelimit.ClientKey`. It is sized as
      `SourceShare` guests (a NATed venue presents every guest as one address), and
      it is what bounds the controller calls a presence check costs. `Look` spends
      only this.
    - `allowDevice` spends a `KeyDevice` bucket keyed on the MAC **and** its source,
      and only after the controller has confirmed the MAC. A MAC nobody vouched for
      never occupies a bucket, and a stranger claiming a present guest's MAC spends
      their own.
    - `TokenBucket` holds the two families in two `ratelimit.NewEvicting`
      limiters: a full table forgets its least recently seen key and **admits the
      newcomer**. Refusing newcomers is a lockout — one laptop naming
      `DefaultMaxKeys` random MACs would lock every later guest out. Eviction's cost
      (a forgotten key's burst is restored) is paid only by somebody holding a
      hundred thousand other sources, each with a budget of its own.

    Proved by `flood_test.go`: a flood of claimed MACs larger than the table, from
    one source and from many, still lets a real guest on; one source spraying MACs
    is one bucket of controller calls; no device bucket exists for an unconfirmed
    MAC; a stranger claiming a guest's MAC does not spend the guest's attempts.

    **It is in-process, and a deployment that needs more is refused.** A
    deployment declaring `ServiceConfig.Replicas > 1` behind a limiter that does not
    implement `SharedLimiter` **does not construct**: N replicas grant N times the
    configured rate, N times the load on the venue's controller, and N times the
    budget in front of a `WHERE code = $1` that is not a constant-time comparison
    and cannot be made one. A limit wrong by a factor of the replica count is worse
    than a process that will not start, because the process that will not start
    names the limit and the reason.

    A `Limiter` that says nothing is read as in-process — the default fails closed —
    and `TokenBucket` implements `SharedLimiter` returning **false** rather than
    relying on that silence. The single-replica assumption is also announced at
    construction even where it is correct: it is invisible from outside the process,
    and the day it stops being true is a day nobody revisits this file.

12. **RFC 8908 media type.** `application/captive+json` on the captive-portal API
    (RFC 8908 errata EID 6620).

    **And it answers for the device that asked.**
    `CaptiveHandler` finds the grant through the portal cookie (`Lookup`) or, for the
    operating system's own API client, which carries none, through the request's
    **transport source address** (`AddressBook`, `address.go`). `captive: false` only
    for a grant whose `StateAt` is `connected`; every other outcome — no session, no
    grant, expired, revoked, `not-on-network`, an address that has meant more than one
    MAC — is `captive: true`.
    `seconds-remaining` is sent when the grant has an end; **`bytes-remaining` is
    never sent**, because the portal holds the allowance and not the usage, and
    `can-extend-session` is never sent. `Cache-Control: no-store`.

13. **Nothing here writes `radacct` or `radpostauth`.** A grant is what the venue
    decided; accounting is what the network observed. They are different objects
    with different lifetimes — a grant exists the instant a voucher is claimed,
    before any accounting row can exist, and on the controller-API path there may
    be no accounting row ever.

14. **The venue's page design is text, rendered from an already narrowed design.**
    `RenderSettings.Design` arrives with hidden and out-of-date blocks already
    removed by `internal/api` — this package reads no date and no visibility switch.
    Every venue string is escaped where it is written; a body keeps its line breaks
    through CSS, never markup. A link is re-checked with `IsLink` where it becomes
    an `href` (http(s) with a host and no userinfo, or `tel:`; **not** `mailto:`,
    which Android's captive-portal WebView blocks), and the accent is re-checked
    against `#rrggbb` where it becomes a stylesheet — the API refused anything else,
    and these are the last gates before bytes. Button and social blocks render only
    on the connected page, because before authorization a link to another origin is
    a button that spins. Markup, order and catalogue keys follow the TypeScript
    renderer (`web/portal/src/render.ts`, with the design rules in
    `web/contract/src/design.ts`); the colour derivation is pinned to the
    TypeScript by `web/portal/src/theme.vectors.json`.

15. **An address is a transport source, never a header, and it never outranks the
    cookie.** `ClientAddress` reads `RemoteAddr` and nothing else — no
    `X-Forwarded-For`, no `Forwarded`. The book is written only by `Resolve` and
    `Lookup` on a session that resolved from its cookie or was just minted, and by
    `issue` when that session is granted; the RFC 8908 query itself writes nothing.
    A second MAC from one address inside the window marks it **ambiguous** and it
    answers as unknown, so NAT or a proxy in front of the portal degrades to the
    pre-existing `captive: true` rather than to another guest's grant. The one
    residual risk is DHCP reusing a departed guest's address while that guest's
    grant is live, and it is RFC 8908 §4's own precondition; it is stated to the
    operator in `docs/portal-https.md`.

    **In-process by default; in PostgreSQL in the product** (`internal/apistore`,
    `portal_addresses`, migration 00045), so a restarted process or a
    second replica answers an online device from the same entry, and an address two
    processes each saw a different MAC from is ambiguous in both. Whatever the book
    cannot answer is `captive: true`, the old answer — a failed read, a miss, an entry
    another process has not refreshed. `MemoryAddressBook` is the rules; the store's
    statements restate them one for one. A caller that builds two `Service` values over
    one deployment (as `cmd/foyer` does) must pass one `Ports.Addresses` to both, or the
    endpoint cannot read what the API middleware observed.

16. **Phone sign-in asks the controller before a message is paid for** (`phone.go`).
    The order is source budget → `VerifyPresence` → device budget → the number, the
    limits and the ceiling (behind `PhoneCodes`) → send; and on verify, source budget →
    `VerifyPresence` → device budget → the code → grant → `303` → Authorize
    (invariant 11). PacketFence, UniFi and Omada check the device at
    grant time only; sending first is a way for a device the venue cannot place to
    spend the venue's SMS budget (§9 toll fraud). A device already online is sent no
    code, for the reason a voucher is not burned for one. **The number never travels
    back to the browser**: the redirect carries `?sent=1` and a reason, never a digit
    of the number, and the page echoes nothing. The code is resolved from this
    browser's session token hash, never from a number or a MAC.

17. **The sign-in forms carry what the venue asks for, and never a ticked box**
    (`render_guestdata.go`). The fields are the venue's, in its
    order; a required one carries `required`. The marketing checkbox has no
    `checked` in any branch, and its hidden `consentVersion` is the version of
    exactly the sentence rendered — resolved like the terms, with **no catalogue
    fallback**, so no sentence is no checkbox. The connected page carries the
    guest's withdrawal form wherever the venue asks for consent, whether or not
    this device consented. `?n=` is allowlisted by `RenderNotice`, like `?e=`, and
    `details-required` / `details-invalid` are reasons with copy in both
    catalogues. The TypeScript twin is `web/portal/src/pages/details.tsx`.

18. **The code field is not the voucher field** (`render_phone.go`). It carries
    `otp-input`, never `code-input`: the enhancement script selects by class and
    groups `code-input` as a voucher, which would turn six digits into `2310-86`,
    which the field's own `pattern` refuses — the browser would block the submit and
    phone sign-in would work only with JavaScript off.
    `OTPPattern` admits everything `otp.NormaliseCode` accepts (digits, spaces,
    hyphens, a no-break space) and `maxlength` leaves room for a pasted, spaced
    code; the TypeScript twin's `OTP_PATTERN` is the same string. The code form
    states the venue's own code lifetime (`RenderSettings.PhoneCodeMinutes`,
    `PortalContext.phoneCodeMinutes`; `DefaultPhoneCodeMinutes` when unknown),
    never a fixed figure, because the lifetime is configurable from one to ten
    minutes.

## The store this package needs

This package does not import `internal/store`. The ports are declared as
interfaces here (`ports.go`) and driven against `memory.go`; the schema below is what
the migrations provide behind them.

### `portal_sessions` — one browser, bound to one MAC

```sql
CREATE TABLE portal_sessions (
    token_hash     text PRIMARY KEY,             -- sha256 of the cookie value, hex
    tenant_id      uuid NOT NULL REFERENCES tenants(id),
    site_id        uuid NOT NULL REFERENCES sites(id),
    nas_id         uuid NULL REFERENCES radius_clients(id),
    vendor_site_id text NULL,                    -- the controller's own site id, not Foyer's
    client_mac     macaddr NOT NULL,
    ap_mac         macaddr NULL,
    ssid           text NULL,
    original_url   text NULL,                    -- allowlisted before it is written
    grant_id       uuid NULL REFERENCES portal_grants(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL
);
CREATE INDEX portal_sessions_expiry ON portal_sessions (expires_at);
```

- **The primary key is the token hash, not an id.** There is no lookup by MAC and
  there must not be one: a lookup by MAC is a lookup by a value the NAS supplies
  unsigned, and resolving a browser's state from a parameter somebody typed is the
  whole failure §4 names.
- `expires_at` is the pre-authentication time limit, enforced **on read** as well
  as by whatever reaps the table. A row past its bound answers `ErrNotFound`, the
  same answer a token that never existed gets — telling them apart would say
  whether a token was ever valid.
- RLS policy in the same migration, like every table except `nas`
  (`docs/rules/database.md`).

### `portal_grants` — what the venue decided

```sql
CREATE TABLE portal_grants (
    id                uuid PRIMARY KEY,
    tenant_id         uuid NOT NULL REFERENCES tenants(id),
    site_id           uuid NOT NULL REFERENCES sites(id),
    nas_id            uuid NULL REFERENCES radius_clients(id),
    client_mac        macaddr NOT NULL,
    ap_mac            macaddr NULL,
    method            text NOT NULL CHECK (method IN ('voucher','account','click-through','phone')),
    voucher_id        uuid NULL REFERENCES vouchers(id),
    account_id        uuid NULL REFERENCES accounts(id),
    plan_name         text NULL,
    plan_name_lang    text NULL,
    duration_minutes  integer NULL,
    data_allowance_mb integer NULL,
    up_kbps           integer NULL,
    down_kbps         integer NULL,
    enforced_by       text NOT NULL CHECK (enforced_by IN ('nas','foyer-sampled','unenforced')),
    delivery          text NULL CHECK (delivery IN ('open','failed')),
    granted_at        timestamptz NOT NULL,
    expires_at        timestamptz NULL,
    revoked_at        timestamptz NULL,

    CHECK ((method = 'voucher')       = (voucher_id IS NOT NULL)),
    CHECK ((method = 'account')       = (account_id IS NOT NULL))
);
CREATE INDEX portal_grants_by_mac ON portal_grants (site_id, client_mac, granted_at DESC);
CREATE UNIQUE INDEX portal_grants_one_per_voucher
    ON portal_grants (voucher_id) WHERE voucher_id IS NOT NULL;
```

- **`portal_grants_one_per_voucher` is the single-use guarantee as a constraint**,
  not only as a transaction. §9 asks for single use "enforced transactionally, not
  with a read-then-write"; a partial unique index is the version of that which
  survives a future caller who writes the read-then-write anyway.
- `enforced_by` is stored rather than derived at read time. It is a fact about the
  moment the guest was let on — the driver's `HardQuota` capability as it was
  then — and re-deriving it would report today's capabilities against last week's
  grant while the number beside it had not changed.
- There is **no plan foreign key**. The plan is copied onto the grant because a
  batch edited afterwards must not change what a guest was already sold.
- **`delivery` is nullable and NULL is the zero value on purpose.** It covers two
  situations this package cannot tell apart: the driver call has not returned yet —
  which is what a *successful* redemption looks like for the length of one
  controller round trip, because the redirect is flushed first — and a deployment
  whose `Authorizations` port is not wired. Both read `connected`, because *nothing
  has told us it failed* is not *it failed*, and a store that lands the column later
  than the Go type reads as the first rather than as an alarm.

### `authorization_events` — what happened when the network was asked

The `Authorizations` port needs one table, and it is the `source: driver` half of
`listAuthAttempts`. The columns are `AuthAttempt`'s, plus the two that make an
`authorize-failed` row actionable — `failure` (`unreachable` | `refused`) and
`status` — carried in `params`.

Two constraints on whatever lands behind it:

- **It writes `portal_grants.delivery` in the same transaction.** The two readers
  must not be able to disagree; see invariant 10.
- **No column may hold the controller's own text.** `nas.APIError` embeds the
  controller URL and up to 512 bytes of its response body; a URL can carry an API
  key in its query string and a body is text somebody else wrote. `Authorization`
  has no field either could arrive in, which is the same shape `AuthAttempt` uses to
  make a password unrepresentable rather than merely unwritten.

It is a **Foyer-owned table**, not an extension of `radpostauth`: that column set is
stock FreeRADIUS's, kept verbatim as the project's exit hatch, and `listAuthAttempts`
says the `portal` and `driver` streams are unioned onto it at read time.

### `ClaimVoucher` is one statement

```sql
UPDATE vouchers v
   SET state         = 'in-use',
       first_used_at = COALESCE(v.first_used_at, $now),
       expires_at    = CASE WHEN b.expiry_mode = 'from-first-use'
                            THEN $now + make_interval(mins => b.duration_minutes)
                            ELSE v.expires_at END
  FROM voucher_batches b
 WHERE v.batch_id = b.id
   AND b.site_id  = $site
   AND v.code     = $code
   AND v.state    = 'unused'
   AND (v.expires_at IS NULL OR v.expires_at > $now)
RETURNING v.id, b.id, b.plan_name, b.duration_minutes, b.data_allowance_mb,
          b.up_kbps, b.down_kbps, v.expires_at;
```

Zero rows means somebody else won, or the code was never claimable. **The reason
is worked out by a second, read-only query** — revoked, expired, already used,
unknown — and that read is deliberately outside the transaction, because it only
chooses which sentence the guest reads.

The claim returns the plan **from the same statement**. Reading the batch
separately would let a batch edited between the two grant a plan the voucher was
not sold under.

The claiming MAC is not written to `vouchers`: it lives on `portal_grants`, the row
that records the decision, and a second copy would be a second source of truth.

### Constant-time comparison, and where it stops

§9 requires it for vouchers. `SameCode` provides it and the in-memory store uses
it. **`WHERE v.code = $code` against an indexed column is not constant time and
cannot be made so** — the index is what makes the portal answer in single-digit
milliseconds. On the SQL path the defences are the `Limiter` in front of it and
the code's own entropy: twelve characters from a 31-character alphabet is a little
over 59 bits, drawn from `crypto/rand`. Written down here rather than assumed.

## `enforcement()` has three inputs

A limited plan behind a driver without `HardQuota` is `foyer-sampled` **only if
accounting actually arrives** — `radacct` byte counters advance on Interim-Update
or Stop and a NAS sends neither unless the server asks in an Access-Accept, and on
the controller-API path there is no Access-Accept at all, so rows exist only where
an operator separately attached an accounting profile (`docs/rules/radius.md`).
Without the third input it would report `foyer-sampled` where the truth is
`unenforced`: a guest told a 500 MB allowance is being sampled every five minutes on
a device from which no counter will ever arrive.

The fact is a NAS measurement — `radius_clients.accounting_configured`, written by
the probe — and `Placement.Accounting` carries it. It is **three-valued**, because
*not measured* is not *not configured*:

| `accounting_configured` | `Placement.Accounting` | limited plan, soft quota |
|---|---|---|
| `true` | `AccountingArrives` | `foyer-sampled` |
| `false` | `AccountingAbsent` | **`unenforced`** |
| `NULL` | `AccountingUnknown` | `foyer-sampled` |

The last row is a guess, deliberately left as the one `radius.md` asks to be closed
at NAS registration: answering `unenforced` for a device nobody
has probed would tell an operator that a working quota is not being applied.

It is read through a join on every session read rather than copied onto the session
row, because `Plan.EnforcedBy` is decided when the grant is made and a probe may
have run since the session was minted. The three placement **ids** are frozen on the
session for the opposite reason — the controller that confirmed a device present has
to be the one asked to let it on.

## Not entry points

`GrantHandler` and `RootRedirectHandler` in `handler.go` are not mounted by
`cmd/foyer`; the contract operations are the portal. `GrantHandler` takes the client
MAC from the request body and has no session behind it, so it must not be mounted.
It refuses on a nil driver and distinguishes the presence outcomes like the
contract operations do.
