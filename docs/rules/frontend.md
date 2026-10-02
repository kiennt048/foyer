# Frontend rules

> Governs `web/` — the contract, the fixtures, the mock, the guest portal, the admin
> console and the end-to-end suite.

`web/` is a pnpm workspace of six packages: `contract` (the OpenAPI document — the
deliverable), `fixtures` (the deliberately ugly world), `mock` (Hono; stateful,
validates every response against the schema), `portal`, `admin`, `e2e`.

Stack: **Preact 10 for the portal, React 19 for the admin** — TypeScript + Vite +
pnpm on both. Measured: React 19 is 60.7 kB gzip for one voucher form against a
50 kB portal budget. Two apps with different constraints; never share a shell.

Evidence authority: [`docs/protocol-reality.md`](../protocol-reality.md). Product
architecture: [`docs/architecture.md`](../architecture.md).

`cd web && pnpm dev` runs all three; starting the portal or the admin alone leaves
it with no mock behind it — no data, and it looks broken. Commands are in
[`CONTRIBUTING.md`](../../CONTRIBUTING.md). Never delete a test to make one pass.

---

## The rule that makes FE-first safe

**You may only display data that `docs/protocol-reality.md` proves exists.** If a
screen wants a field that is not recorded there, the answer is not "mock it" — go
measure it, or design without it.

This is not bureaucracy. It is the specific failure mode of FE-first in a
protocol-constrained domain: a beautiful admin UI showing a live per-client
bandwidth graph, built on accounting that arrives every five minutes and on many
deployments never arrives at all.

Two consequences that are already load-bearing in the code:

- **Any figure derived from accounting travels with its freshness** — the sampling
  interval beside the number, "sampled 40 days ago" where that is the truth. A bare
  figure is the portal promising a cap that nothing on the network applies.
- **Never subtract two byte counters without handling the 32-bit wrap.** The
  `sess-counter-wrap` fixture exists so the first delta, rate or "remaining" figure
  anyone adds goes visibly negative rather than quietly wrong.

---

## web/portal — the guest portal

### Fixed constraints

**The portal must render before the client has internet.** No CDN fonts, no
external stylesheets, no third-party scripts, no analytics — everything in the
bundle, and a build assertion scans the output for absolute URLs. Any external
resource you add must also be added to the walled garden on *every* driver, which
is why the answer is to not add one. (The rule is **no external origin**, not *no
linked asset*: a same-origin asset such as a venue photograph is reachable by
construction — see `docs/protocol-reality.md`.)

**It is a multi-page application.** The CNA re-checks captivity only after a full
URL navigation; without one its button stays **"Cancel"**, and Cancel disassociates
the device. A `fetch`-submitted login authenticates the guest and then throws them
off the network — and Playwright reports a pass. Every step is a real document
navigation, `POST` answered `303`. **Not reversible**: it shapes every route and
the contract itself.

**CSS and JS are inlined, never linked.** Android calls `clearCache(true)` on every
portal launch and the CNA has no persistent store, so a linked asset is refetched
every time and a content hash buys nothing. One HTTP request per document.

**The flow completes with JavaScript disabled.** JS is enhancement — formatting, a
countdown, validation. It never carries the flow.

**And it must complete with JavaScript enabled.** The enhancer selects fields by
class, so **a class is a behaviour**: each field kind gets its own (`code-input`
voucher, `otp-input` one-time code) — sharing one would let the voucher formatter
regroup a one-time code into a value the field's own `pattern` refuses. A `pattern`
admits everything the server's normaliser accepts, and the proof is a browser driving
the enhanced field to the connected page (`web/e2e/tests/portal/phone-code.spec.ts`),
not a markup test.

**Nothing the guest must see may come after authorization.** Android destroys the
login WebView the instant the network validates. Terms, session detail and venue
links come *before* the grant call.
The narrow reading: this governs what a guest **must** see. A venue's review or
social-media link on the page after sign-in is optional marketing that many Android
guests will never see, and the console says so where the operator places it — it
cannot go before sign-in, where the phone cannot reach it.

**Budget ≤ 50 kB compressed with the system font stack, ≤ 90 kB with a webfont.**
CI hard-fails (`web/tools/check-budget.mjs`). The heaviest document as built is a few
kilobytes. A venue's page blocks are rendered per request and are weighed in the
console, not by this gate.

### What the CNA actually is

"The CNA has limited JavaScript" is **wrong**. Static analysis of the shipped macOS binary shows a plain `WKWebView` on
system WebKit, and the CNA injects JavaScript of its own. The engine is complete;
the **environment** is stripped — an ephemeral `WKWebsiteDataStore` makes cookies,
`localStorage`, IndexedDB and service workers memory-only and destroys them on
close.

Designing for a crippled interpreter solves the wrong problem. The real
constraints:

- **No state survives the portal.** All state is server-side and grant-bound; the
  client stores nothing it cannot lose. Language is a URL path segment, not a cookie.
- **No popups and no tabs.** `target=_blank` and `window.open` replace the page with
  no way back. `mailto:`, `intent://` and `market://` are hard-blocked on Android;
  `tel:` and `sms:` are the only non-http schemes that work.
- **App switching disconnects the device.** No flow may send the guest to an
  authenticator or an SMS app and expect them back.
- **A cross-origin round-trip loses its state on return.** That — not a redirect
  block — is why OAuth and social login cannot work here.
- The macOS CNA window is **900 × 550 pt and does not grow**. iOS is not yet
  measured from a log, so 550 pt is a floor that happens to be measurable, not the
  worst case.

**Playwright cannot emulate any of this.** Portal E2E going green means nothing
about real phones. **Testing the portal on a real phone, inside a real CNA, is not
skippable and not substitutable** for any change to the guest flow.

### i18n

English is the default and the source of truth: `en.ts` *defines* the message key
type and `vi.ts` is typed as a complete map of it, so a forgotten translation is a
compile error. The admin console speaks English and Vietnamese (below). Terms are a
per-language map, never paraphrased and never machine-translated.

**What the console prints is in the venue's language, not the console's.** A voucher
slip is read by a guest, so it is printed in `PortalSettings.languages`' first
language from the slip half of `web/admin/src/i18n/print.ts`, typed as a total map of
its English keys like the portal's `vi.ts` — whatever language the operator reading
the screen beside it has chosen.

The voucher field — the most-used control in the product — is often typed through
an IME (Vietnamese is a shipped language). **Never write to an input's `value` while
`isComposing` is true**; Gboard and Vietnamese input methods compose in the field
and read it back, and assigning underneath them drops or duplicates characters.
The guard is invisible, which is why it is written down.

---

## web/admin — the admin console

### Copy is split into catalogues, and which one a key goes in is a byte decision

`src/i18n/` is `core.ts` plus one namespace per lazily loaded feature. `t` is generic
over `keyof` whichever catalogue the importing module gets it from — `i18n/index.js`
resolves core, `i18n/portal.js` resolves core plus the portal screen's — so a key that
does not exist, or that belongs to another screen, is a **compile error**.

**The rule is about what a browser downloads, not about what a thing is called.** A key
used by anything in the entry chunk lives in `core.ts` whatever its prefix — the
access-point *list* is eager, so `nas.col.*` is core while `nas.rotate.*` is in
`nas.ts`. A key used by exactly one lazy feature lives in that feature's namespace. A
key used by two lazy features goes back to `core.ts`, because a module imported from two
chunks becomes a third chunk and a third request costs more than the bytes.

It exists because Rollup drops unused modules, not unused properties: a single
catalogue would ship every lazy screen's copy in the entry chunk. The budget in
`web/tools/check-admin-budget.mjs` binds on the shell's growth rather than on any
screen's.

Getting it wrong is not silent in either direction: a screen's key in `core.ts` grows the
entry, and a shell key in a namespace stops the shell compiling.

### The console in two languages — how a translation arrives

**English is the source of truth**; each catalogue `i18n/<ns>.ts` has a Vietnamese twin
`i18n/vi/<ns>.ts` typed `Translation<typeof <ns>>`, so a missing key or a wrong
parameter list does not compile, and `i18n/i18n.test.ts` repeats the check over the
directory at runtime. A new catalogue without its twin fails that test the moment the
file exists.

**The language** is the stored choice (`localStorage` `foyer.admin.lang`), else the
first of the browser's languages the console speaks, else English — Keycloak's and
Grafana's order. A change **reloads**, as both of theirs do. Per browser, not per
operator. The venue's portal language is not consulted: it is the guests'.

**Every translation is loaded lazily** (an English reader downloads none of it) and
that imposes three rules, each held by a test that fails when broken:

- **No `t()` at a module's top level.** A module's top level runs before its
  translation can have arrived, so a label map built there is English for a
  Vietnamese reader for the life of the page. Use a getter (`get label() { return
  t("…"); }`), `lazyCopy(() => ({ … }))`, or compute it in the render. The AST scan in
  `i18n.test.ts` refuses the call.
- **Every dynamic `import()` of a module goes through `withCopy`**
  (`withCopy(() => import("./screens/X.js"))`), which resolves only once the
  translations that module's namespaces asked for are installed. The same scan
  refuses a bare one.
- **No top-level `await` for any of it.** A top-level `await` in a namespace makes
  the bundler split the entry so that no lazy chunk statically imports an async
  module, which costs requests and bytes. `loadLocale` registers a promise instead and
  `main.tsx` renders after `localesSettled()`.

`web/tools/check-admin-budget.mjs` refuses a build that puts any `vi/` catalogue in the
entry chunk. `print.ts` still imports nothing: its operator half reads the language
from `<html lang>`, which `lang.ts` sets before anything lazy loads, and the kit
exports `printCopyReady` for the sheet to await.

### Settings: what a venue sets up front, technical knobs behind Advanced

A settings page of dozens of inputs laid flat in milliseconds and bytes is a wall,
not a form. The problem is the flat wall, not the configurability, so nothing is
removed. UniFi, Omada, Meraki, authentik and Keycloak all do the same thing: the
settings a site owner changes on the first screen, protocol figures under one
collapsed **Advanced**, units in words, the default beside each, and a reset.

- `ui/settings.tsx` is the shape: `Advanced` (a `<details>`, closed by default) and
  `SettingNumber` (the shown unit, `min`/`max` in it, "Between … Default …" from the
  server's bound, "Reset to default" on a knob that has moved).
- **Every range and every default comes from the server.** `NetworkSettings.defaults`
  is per member because the four throttles do not share a default;
  `listCoaTiming` carries `bounds`; the phone-abuse limits and the SMS cap carry
  `bounds` read by the server from the contract's own `minimum`/`maximum`. A range
  restated in the console is a second copy nothing holds to the first.
- It is shared by three lazy screens, so it is a shared chunk — against the rule above
  that a key used by two lazy features goes to `core.ts`. Deliberate: the extra request
  is paid on three rarely opened settings screens; `core.ts` and an eager component
  would be paid by every operator on every load.

### Scope discipline

**A screen is designed against an operation that exists in the contract.** Do not
design screens for systems the backend does not implement — social or OAuth sign-in,
a general-purpose PKI, an issuing CA for guests. Their constraints are unknown, and
that is exactly where wasted frontend work concentrates. Where a system does exist
(the portal and EAP certificates, the SMS provider for phone sign-in), its screen
covers what the contract's operations do and nothing more.

### Capability-conditional UI — hide by default, never disable

Controls **disappear** based on `NASDriver.Capabilities()`. A disabled control still
advertises a feature the hardware does not have. The one exception is a capability
that exists but is currently unavailable: disabled, with the reason stated.

But a capability describes what the **driver** can do on the operator's behalf, not
what the product may offer: ask whether a false one makes the action *impossible*
(hide it) or merely *manual* (keep it, and state the price beside the control, before
the operator commits). `RotateSecret` gates only the **push** — hiding **Generate new
secret** would leave that NAS unable to rotate a secret at all. Prefer the narrower
reading of a capability, as with *UniFi cannot do CoA* ([`nas-drivers.md`](nas-drivers.md)).

**A one-time value is one component, and the acknowledgement lives in the caller.**
Registration and rotation both mint a RADIUS shared secret the operator must carry to
a device, and both render it through `screens/nas/SecretReveal.tsx`. **Two renderings
of one disclosure drift, and the one that drifts is the one nobody returns to** — a
rotation that discarded the value would leave a device on a driver that cannot push
the secret unable to authenticate, with no operator able to configure it.

The protection on that surface is not the absence of a Cancel button. It is that
`onContinue` is the caller's refusing function and `Modal`'s `onClose` *is* that
function, so Escape, the scrim and the primary button are one thing; the
acknowledgement is the caller's state, because a checkbox the dialog owns is a
checkbox the dialog can lose. Copying ticks it — copying *is* storing it — and the
checkbox is there for the operator who stored it some other way, because a flow that
only accepted one of those would push them into lying to it.

**And the value is cleared on `pagehide`,** because bfcache preserves the DOM and the
JS heap: a restored page would come back with the dialog still holding a live
credential and the acknowledgement still ticked. The wizard does not need this — it
navigates on success and `AccessPoints.tsx` removes `/new` from the history stack —
so rotation is where the guard lives. A value not in the heap is not in the snapshot.

| Capability false | UI must |
|---|---|
| `BandwidthLimits` | hide the rate-limit fields entirely |
| `DynamicVLAN` | hide VLAN assignment |
| `HardQuota` | show quota as *sampled*, with the interval, not as a hard cap |
| `LiveReauthorize` | no "apply now" — changes take effect on reconnect |

A mocked admin UI will happily render every control for every NAS. **That is the
easiest thing to get wrong here and the most expensive to discover later.** Every
mock NAS fixture includes a low-capability driver, and a test computes the expected
control count from the capability model rather than from a hardcoded number.

### Read-only regions

`radacct` and `radpostauth` belong to FreeRADIUS. Accounting and session history
are **read-only, forever**. Do not design "edit session" affordances; they can
never be built.

**One action exists on a session and it is not an edit:** *disconnect the guest*,
which asks the **device** to end the session through its driver and writes no
accounting row — the device's own Acct-Stop closes it. It is gated on
`Capabilities.canDeauthGuest`, says why beside the missing control where a device
cannot (Ruckus, by decision), lives behind a confirmation outside the dock, and is the
one operation `contract/src/read-only.ts` names as an exception. A second write under
`/sessions` still fails that guard.

---

## Both applications

**Colour reaches a component only through a role-named token.** The role name says
*why* the colour is there, which is what lets the dark theme be a theme rather than
an inversion. Literal colours live only in `admin/src/styles/tokens.css` and
`portal/src/styles/portal.css`; `web/tools/check-tokens.mjs` fails the build otherwise.
Colour is spent on status and one primary action per page, nowhere else — and colour
is never the only signal.

**Fixtures are ugly on purpose.** If they are tidy, a design review is worthless: it
validated a world that does not exist. Every fixture set keeps a session with no
`Acct-Stop` "online" for 40 days, interim updates 20 minutes stale, MACs with no
hostname and randomised OUIs, duplicate sessions for one MAC and a non-unique
`Acct-Session-Id`, clock skew, a wrapping byte counter, an unreachable NAS, one with an
expired API key, one refusing `require_message_authenticator`, vouchers
expired/exhausted/revoked mid-session, and a driver with `WalledGarden: none` and
`BandwidthLimits: false`. **A moving fixture that heals itself is worse than a static
one** — it removes the case it exists to carry.

**And a fixture world can be ugly in every dimension it has and still be blind in
one.** If every session, account, device and voucher batch lives in one site, a
dropped site scope changes no answer, and no check — the schema-validating mock, the
E2E suite, the differential harness, a design review — can see it. So the fixtures
populate more than one site:

- **Ask which *cross-cutting* assertion a fixture makes possible**, not only which ugly
  value it carries. Rendering is not the only thing a fixture is evidence for.
- **A second site must be populated, not merely present.** An *empty* second site
  differs from a populated one just as convincingly as correct scoping does. The guard
  that holds is a partition invariant: every row belongs to exactly one site, and the
  totals add up.

**The OpenAPI document is the deliverable.** `contract/openapi.yaml` is the only
place an operation is defined; `generated/types.ts` is generated and never edited;
mock handlers are typed against it, so a handler for a nonexistent operation does
not compile; and the mock validates every **response** against the schema at runtime.

**It does not validate requests.** `validate.ts` exports `conformResponse` and
`conform`; no route validates a request body, so a required request field does not
fall out of the schema and a check for it in the mock is a hand-written second copy
of a contract rule ([`database.md`](database.md)). Treat request validation as missing
rather than as present. If you are writing a handler with no corresponding operation,
stop and write the operation first.

---

## Review checkpoints

A change to what a guest or an operator sees is reviewed at three points, each on the
surface it changes:

1. **Portal flow + aesthetics** — clickable, in a desktop browser, in both languages.
2. **Portal on a real phone, inside a real CNA.** Non-negotiable for any change to
   the guest flow; Playwright cannot stand in for it (above).
3. **Admin screens + the object model** — nouns, ownership, what is read-only, what
   is capability-gated. The model is what gets reviewed; the pixels come with it.

Attach screenshots (and, for the portal, the device and OS) to the pull request.

## Security

No `dangerouslySetInnerHTML`. Never render a NAS-supplied value as a link target
without validating the scheme ([`security.md`](security.md) §4 — the portal is a prime
open-redirect target precisely because users expect to be redirected). Never put a
username or password in a URL. API errors show a human message, never a raw error or
internal path.
