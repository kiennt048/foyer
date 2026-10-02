# `@foyer/e2e` — end-to-end suite for the portal and the admin UI

## What this suite cannot do

> **Playwright cannot emulate the Captive Network Assistant. Portal E2E going green
> means nothing about real phones.**

That is not a caveat, it is the operating assumption. `docs/rules/testing.md`
states it and `docs/protocol-reality.md` records the measurement behind it.

Specifically:

- The **Cancel-instead-of-Done** failure — the CNA never re-checks captivity, keeps
  showing "Cancel", and Cancel disassociates the device — is **invisible to
  Playwright** and fatal on hardware.
- Playwright cannot tell you whether the CNA honours `prefers-color-scheme`, whether
  `@font-face` loads, what the iOS viewport is, or whether the window auto-dismisses
  on `captive: false`. Five such questions are open in `docs/protocol-reality.md`
  and each is minutes of work **with a phone in hand**.

So this suite's job is narrow and worth stating: **it prevents regressions in what
is already understood.** It is not a substitute for testing the portal on a real
device, inside a real CNA, and that test is not skippable.

## The one test that matters most

`tests/portal/navigation-not-xhr.spec.ts`.

The portal's login must be a real `<form method="post">` answered `303 See Other`,
producing a second document. If it ever becomes a `fetch` + re-render, the guest is
authenticated and then thrown off the network — and **the obvious Playwright test
passes either way**, because both endings put the success markup on screen.

So that spec asserts the *mechanism*:

1. the request to the authenticating endpoint is `resourceType: "document"` and
   `isNavigationRequest()`;
2. it is answered **303** with a `Location`;
3. a `framenavigated` **and** a `load` follow it;
4. **no `fetch`, XHR or `sendBeacon` touches the API at all** — `fetch`,
   `XMLHttpRequest.prototype.open` and `navigator.sendBeacon` are wrapped from
   inside the page before any page script runs (`tests/helpers/network.ts`).

The long comment at the top of that file explains why a normal assertion would be
worthless there. It is there for the person who will one day try to simplify it.

## Running it

```bash
cd web
pnpm install
pnpm -F @foyer/e2e install:browsers     # playwright install chromium

pnpm test:e2e                            # everything
pnpm -F @foyer/e2e test:e2e --project=portal
pnpm -F @foyer/e2e test:e2e --project=admin
pnpm -F @foyer/e2e test:e2e --project=a11y-dark --project=a11y-light
pnpm -F @foyer/e2e report                # open the HTML report
```

The suite **starts the stack itself** via `webServer` (mock :5170, portal :5171,
admin :5173). Outside CI it reuses servers that are already running, which is the
normal case during development — `pnpm dev` in `web/` starts all three.

Override the origins with `FOYER_MOCK_URL`, `FOYER_PORTAL_URL`, `FOYER_ADMIN_URL`.
`playwright.config.ts` reads the port each server is started on out of the URL it is
checked on, so an override moves both. To run the servers yourself on other ports,
no file needs changing — the mock reads `PORT` and `PORTAL_ORIGIN`, and both vite
configs read `MOCK_ORIGIN`:

```bash
PORT=5270 PORTAL_ORIGIN=http://localhost:5271 pnpm -F @foyer/mock dev
MOCK_ORIGIN=http://localhost:5270 pnpm -F @foyer/portal exec vite --port 5271 --strictPort
MOCK_ORIGIN=http://localhost:5270 pnpm -F @foyer/admin exec vite --port 5273 --strictPort

FOYER_MOCK_URL=http://localhost:5270 FOYER_PORTAL_URL=http://localhost:5271 \
FOYER_ADMIN_URL=http://localhost:5273 pnpm test:e2e
```

Browsers are installed into Playwright's shared per-user cache (see Playwright's
documentation for its location on your platform). If a run fails with
`Executable doesn't exist at …/chromium_headless_shell-…`, the cache was emptied;
`pnpm -F @foyer/e2e install:browsers` restores it.

### Ports and stale dev servers

The suite uses ports **5170, 5171 and 5173**. Make sure nothing else holds them —
another `pnpm dev`, or a server left running from an older checkout.
`reuseExistingServer` attaches to **whatever** is listening there, so a run can test
a different tree from the one you are editing, or a dev server older than your source.
Both look like product bugs.

- Before trusting a surprising result, confirm the server is serving the source you
  are reading: restart it, or `curl` the page and grep for a string from your newest
  edit.
- The suite pins the mock's sign-in password with `FOYER_MOCK_ADMIN_PASSWORD` in its
  `webServer` entry, and that variable reaches only a mock the suite started. A run
  attached to some other mock therefore fails every sign-in rather than silently
  testing it.
- Playwright stops the `webServer` processes it started when its run ends, so a
  second run that attached to them loses its servers mid-suite. Run one suite per
  stack.
- Stop a stray server by its pid (`lsof -nP -iTCP:5170 -sTCP:LISTEN`), never with a
  `pkill -f` pattern that would also match other checkouts' servers.

A run that fails with timeouts and **no assertion** behind them is usually a loaded
machine rather than a regression; re-run the failing spec on its own before reading
the trace.

### A `page.route` that matches nothing is not an error — it is a passthrough

Playwright matches a `RegExp` route pattern against the **whole URL**, not the path.
So `/^\/api\/v0\/operators\/…/` matches nothing at all against
`http://localhost:5173/api/v0/operators/op-viet`, and **an interception that matches
nothing does not fail — it lets the request go to the server**, where it mutates the
shared mock.

**Use a `URL` predicate rather than a bare `RegExp`**, and where a test asserts a count
of anything append-only, assert it **before and after** so an interception that stopped
working is loud rather than a silent write.

## Projects

| Project | Covers | Notes |
|---|---|---|
| `portal` | `tests/portal/**` | 390×844 viewport, dark scheme. One describe block runs with `javaScriptEnabled: false`. |
| `admin` | `tests/admin/**` | 1440×900, dark scheme. |
| `admin-narrow` | `tests/admin-narrow/**` | 390×844, dark scheme. The admin's *second layout* — see below. |
| `admin-auth` | `tests/admin-auth/*.spec.ts` | 1440×900, dark scheme. The routes that care *who* is signed in — `/sign-in`, `/operators`, `/audit`, `/password` — with no project-level `storageState`: each test signs itself in through `helpers/session.ts` and says which operator it is, so a refusal (`403`) is reachable. |
| `admin-auth-narrow` | `tests/admin-auth/narrow/**` | The same routes at 390×844. `/sign-in` renders outside the console shell, so it is a separate layout to lock. |
| `a11y-dark` / `a11y-light` | `tests/a11y/**` | axe over every route in both applications, once per colour scheme. |
| `a11y-narrow` | `tests/a11y-narrow/**` | axe over the admin routes at 390×844, **dark only**. |

Three of those directory names are load-bearing rather than tidy.
`tests/admin-narrow/` and `tests/admin-auth/` do not contain the substring `admin/`,
and `tests/a11y-narrow/` does not contain `a11y/`, so the desk-width projects do not
also claim those files. Renaming one to something that *does* contain the shorter name
(`narrow-a11y/`, for instance) silently doubles its tests into a project that cannot
pass them. `admin-auth`'s pattern is `[^/]+` rather than `.*` so it does not reach
into `admin-auth/narrow/`.

## The admin has two layouts, not one layout at two sizes

Below 900px the sidebar is a sticky header, the navigation moves into a drawer,
every table drops most of its columns into a per-row disclosure, and the keyboard
hints hide on **input capability** rather than on width. None of that markup exists in
the `admin` project's 1440px renderings, so none of it can be covered there.

`responsive-shell` asserts that `document.scrollWidth` equals the layout viewport on
every operator route it lists. It is paired with `narrow-columns-fit`, because each can
miss the other's failure: `.table-scroll` absorbs a table wider than its port, so the
*page* can fit at 390 while a table inside it does not. One file measures the
document, the other the scroll port. The second runs its tables **under both pointer
models**, because `.row-disclosure` is 28px under a fine pointer and 36px under a
coarse one.

**A width assertion that has never been red proves nothing**, so each lock ships its
revert: `the lock is not vacuous` injects the reverted width declarations with
`addStyleTag` and requires every route to overflow, and `the readability check is not
vacuous` reverts the disclosure list's `width: 100cqw` and `position: sticky` and
requires the list to grow wider than its port — because `toBeVisible()` passes for a
list that starts three quarters off the right-hand edge.

**`/design` is excluded from the width lock and says so out loud**, with all three
reasons asserted — absent from `NAV`, lazily loaded, and its widest overflowing box is
a `.gal-theme` panel rather than anything inside a table. A route silently missing
from a coverage list reads as coverage.

Two harness facts:

- **`hasTouch: true` is what makes `(hover: none) and (pointer: coarse)` match**, not
  `isMobile`. `isMobile` alone changes the viewport meta and the user agent and leaves
  the pointer model fine. The query is asserted as a precondition in
  `touch-actions-visible.spec.ts`.
- **`page.setViewportSize` delivers `resize` and `MediaQueryList` `change`**, one each,
  in both directions — on a later turn than the call resolves, so it must be polled:
  reading a counter synchronously after `setViewportSize` reports zero.

## The exemption inside the capability gate

`capability-gating.spec.ts` derives its expected control count from the NAS's own
`Capabilities`, so it fails when the gate and the model diverge rather than passing
against a number somebody typed.

**"Generate new secret" is deliberately not capability-gated.** `rotateSecret` is
the driver's ability to *push* a secret onto the device; *generating* one is Foyer's
own operation and works everywhere. Hiding the button when the capability is false
would leave that site unable to rotate a shared secret at all, and a secret that cannot
be rotated is one that never is. The console keeps the control and states the price
beside it.

A model-derived test would embody the reasoning that says the button should
disappear, so the exemption is pinned explicitly by three tests:

- the button exists for every driver that cannot push, and is enabled wherever the
  device is still reachable;
- the consequence is **standing on the page beside it**, not only inside the
  confirmation — asserted through `data-manual`, and by comparing the page's two
  renderings of the sentence to each other rather than to any literal;
- the button is asserted **not** to match the gated-control locator, so the
  exemption is expressed relative to the gate instead of restated beside it.

Wrapping the button in `<IfCapable need="rotateSecret">` turns all three red while the
original gating tests stay green.

The companion case is `rotateNasSecret`'s **409** — refused only where the driver
cannot push **and** the device is unreachable, the one combination with no route
back. `web/mock/src/server.test.ts` drives it without a socket; the E2E case drives
it through the console and asserts no secret is put on screen for a rotation that
did not happen.

## Two things that shape every test here

**The mock is stateful.** Redeeming a voucher consumes it; revoking one ends a
session. So no test hardcodes a voucher code — `freshVoucherCode()` asks the mock
for an unused one at the moment it is needed, falling back across three batches, and
says plainly when they are drained. For the same reason the suite runs with
`workers: 1`: two workers racing for "the first unused voucher" would produce a
flake that reads like a product bug.

A voucher is *consumed*, which the fixtures budget for. **A registered access point is
different: it is a row that stays**, and it changes what every later row count,
dashboard figure and screenshot describes. So a test that registers one deletes it,
through `expectNasRemoved`, which fails if the device is not there to delete.

**No assertion reads user-facing copy.** Both applications ship in more than one
language and their copy changes. A test that asserted a label would fail on a
translation rather than on a regression. Everything selects by role,
structure, `href`, URL, or a semantic `data-*` attribute — `data-level="negative"`
on a status pill says what a row *means* without reading a word of it.

If a test ever cannot avoid it, the right move is to add a `data-testid` to the
component, not to assert on a string.

## Accessibility: gated, everywhere, no flag

`tests/a11y/routes.spec.ts` runs `@axe-core/playwright` over every route in both
applications and both colour schemes, attaches every violation to the report, writes
`test-results/a11y-<project>.json`, and **fails the run on any WCAG violation
anywhere**. Both applications are measured at zero.

When it fails on contrast, look at the colour token before the rule: a contrast
finding across many elements is usually one token value. The summary printed on
failure exists to tell those two cases apart. Do not add a reporting mode to land a
change that breaks this.

Only WCAG tags are counted (`wcag2a`, `wcag2aa`, `wcag21a`, `wcag21aa`,
`wcag22aa`). `best-practice` is excluded on purpose: mixing advice into a number
that is meant to reach zero is how the number stops meaning anything.

`/connected/` appears **twice** on purpose — once uncookied (the honest "not
connected" variant, a real state a guest reaches) and once after a real redemption —
so both the success page and the not-connected page are covered.

### The sweep walks URLs, so what has no URL is where a regression hides

Because the gate fails on any violation anywhere, **the surfaces the sweep cannot
reach are the only place one can survive.**

`RouteSpec.setup` runs *before* `page.goto`, so it can only arrange what the server
will answer with — a cookie, a grant, a redemption. **`RouteSpec.after` runs after
`goto` and before `ready`**, which is what a step reachable only by clicking needs.
The registration wizard uses it for steps two and three; step one has `/access-points/new`
and needs nothing. Steps two and three each register a device, and `afterEach` deletes
it — `expectNasRemoved` asserts the deletion rather than attempting it, and its one
exemption is a mock that restarted mid-run, told apart by the tick in
`X-Foyer-Mock-Clock` going backwards.

**What the sweep still cannot see**, in rough order of what it would cost to add:

| Surface | How it is reached | Note |
|---|---|---|
| Session detail dock | `/sessions?session=<id>` | **Has a URL already** — one row, no `after`. |
| Portal, rejected voucher code | `/{lang}/voucher/?e=…&code=…` | A URL too. The error variant of the densest portal form. |
| Portal, single-language venue | `foyer_mock_venue` cookie, then `/vi/` | `setup` can do it; renders *no* switcher, a different document. |
| Shortcut sheet | `Mod+/` on any screen | Its own search field and a long definition list. |
| Batch action bar | check a row on `/sessions` | Appears over the table and shifts the dock. |
| Create-voucher-batch, create-account | the page's primary action | Two forms with no other coverage. |
| Rotate-secret / delete-NAS confirmations | a control on `/access-points/{id}` | The *markup* is swept flat on `/design`; the live modal is not. |
| Wizard probe failures | register with a `192.0.2.x` or `198.51.100.x` address | Three notices and an unmeasured verdict, none of them scanned. |

The list is the point: a sweep that grew by three routes reads as a sweep that is
finished.

## Layout

```
tests/
├── portal/                          project `portal`
│   ├── navigation-not-xhr.spec.ts   the reason this package exists
│   ├── no-javascript.spec.ts        the whole flow with JS disabled
│   ├── enhanced.spec.ts             grouping, errors, language switch
│   ├── phone-code.spec.ts           phone sign-in with JavaScript enabled
│   ├── rejected-code-echo.spec.ts   a mistyped code comes back in the field
│   ├── terms-and-languages.spec.ts  the consent sentence; the venue's languages
│   └── verify-presence.spec.ts      an unconfirmed client; the NAS query string
├── admin/                           project `admin`
│   ├── happy-paths.spec.ts          the paths an operator actually walks
│   ├── first-run.spec.ts, create-first-venue.spec.ts     an empty deployment
│   ├── capability-gating.spec.ts    the gate, and the one control exempt from it
│   ├── coa-vendor.spec.ts           a CoA device is asked which vendor it is
│   ├── session-detail.spec.ts       the detail dock: linkable, sticky-dismissed, inert
│   ├── session-disconnect.spec.ts   disconnecting a guest from the dock
│   ├── portal-page-builder.spec.ts  the portal page builder
│   ├── settings-advanced.spec.ts    technical knobs behind Advanced
│   ├── privacy.spec.ts              retention and erasure
│   ├── language.spec.ts             the console's language
│   ├── voucher-print.spec.ts        the printable sheet
│   ├── site-survives-a-reload.spec.ts       the chosen venue persists
│   ├── absence-is-not-zero.spec.ts, offline-does-not-invent-an-answer.spec.ts,
│   │   error-architecture.spec.ts   failures look like failures
│   ├── fields-are-styled.spec.ts, status-is-not-only-hue.spec.ts,
│   │   wide-table-says-what-is-hidden.spec.ts
│   ├── overlays.spec.ts             every overlay is `showModal()`, not `<dialog open>`
│   │                                — the keyboard and focus locks:
│   ├── dialog-focus-return.spec.ts  overlays hand focus back, on Escape and Cancel
│   ├── confirm-dialog-focus-return.spec.ts  confirmation dialogs, at every call site
│   ├── dialogs-are-named.spec.ts    every `dialog[open]` resolves an accessible name
│   ├── shortcuts-through-a-modal.spec.ts    shortcuts do not fire through an overlay
│   ├── palette-is-an-overlay.spec.ts        the trap, the inert document, Escape
│   ├── row-activation.spec.ts       Enter and Space on the rows that are tab stops
│   ├── cursor-move-keeps-focus.spec.ts      one ArrowDown does not leave the list
│   ├── row-count-announced.spec.ts  the live region names the count, and stays quiet
│   ├── pending-does-not-eat-focus.spec.ts   a pending control keeps focus
│   └── destructive-action-restores-focus.spec.ts  where focus goes when the row is gone
├── admin-narrow/                    project `admin-narrow`: the second layout
│   ├── responsive-shell.spec.ts     the routes fit 390px — and the revert that proves it
│   ├── narrow-columns-fit.spec.ts   the *table* fits its own port, both pointer models
│   ├── drawer-focus.spec.ts         opens, traps, Escapes, hands focus back
│   ├── narrow-values-reachable.spec.ts  a dropped column is one tap away, and readable
│   ├── touch-actions-visible.spec.ts    copy/filter exist for a thumb; hints do not
│   ├── attempts-says-why.spec.ts    the phone view of /attempts says why
│   └── first-run.spec.ts            first run at phone width
├── admin-auth/                      project `admin-auth`: who is signed in
│   ├── sign-in.spec.ts, password.spec.ts, forced-password-change.spec.ts
│   ├── role-gating.spec.ts, last-administrator.spec.ts, operators-pending.spec.ts
│   ├── operator-overlays.spec.ts    the cancel control, per confirmation tier
│   ├── audit.spec.ts, reports.spec.ts, live-regions.spec.ts
│   ├── session-spans-both-origins.spec.ts
│   └── narrow/
│       └── auth-routes-at-390.spec.ts   project `admin-auth-narrow`
├── a11y/
│   └── routes.spec.ts               axe over every route, both schemes
├── a11y-narrow/
│   └── routes.spec.ts               axe at 390px: the routes, the drawer, an open row
└── helpers/
    ├── env.ts       origins, the mock password, language helpers
    ├── mock.ts      stateful-fixture lookups against the mock API
    ├── network.ts   the two-sided network recorder
    ├── portal.ts    portal selectors and flow steps
    ├── admin.ts     admin selectors
    ├── fields.ts    what makes a refused field actionable
    ├── session.ts   signing a test in as a named operator
    └── overlays.ts  the overlay and confirmation inventories, and how to open one
```

## The focus locks, and the things they are written around

They cover what axe cannot see: focus order, focus return, and keyboard behaviour.

`helpers/overlays.ts` holds the two inventories — the overlays and the
`ConfirmDialog` call sites — and three specs drive them, so a new overlay is covered by
all three the moment it is added there. **Nothing is a count.** `/controllers`'s probe
result has no cancel control at all, and `deriveCancelControl` finds that out by
pressing the candidates with the API aborted rather than by being told.

Harness facts:

- **The focus restore runs in React's passive cleanup**, one task after the `<dialog>`
  leaves the DOM. A raw `evaluate` of `document.activeElement` the instant
  `dialog[open]` hits zero reads the old node and reports a working fix as `<body>`.
  Every assertion here is `toBeFocused()` or `expect.poll`, which retry.
- **Every modal `<dialog>` tab cycle includes exactly one press landing on `<body>`
  before it re-enters.** That is Chromium, not a defect — so the stops are asserted
  *inside* the dialog and nothing counts presses.
- **`/accounts`' confirmation is the type-name tier**, which renders no Cancel and
  keeps its danger button `disabled` until the username is typed. Both the cancel lock
  and the shortcut lock derive that rather than working around it.
- **A `\b` in a source-scanning regex matches between the hyphen and the `d` of
  `aria-disabled`**, so the obvious pattern reports every repair as the defect. That
  one belongs to `admin/src/screens/screens.test.ts`, which is the source half of two
  of these locks; this package is the behavioural half, because `@foyer/admin` has no
  jsdom and cannot observe focus at all.

**Most locks ship their own sabotage**, in the suite, so the evidence re-derives on
every run: `dialog.remove()` for the overlay that leaves the DOM while still open; a
capture listener that swallows a row's `keydown` before React sees it;
`el.disabled = true` on the focused control; the palette closed and re-opened the
non-modal way; the live region taken out of the document; the naming attributes
stripped. The two that cannot be re-injected from the page — a cursor move is not
deliberate, and the refocus hook is armed at a press — describe in their file headers
the source change that turns them red.

`row-activation` asserts both halves — `Enter` and `Space` work on tables whose rows
are a tab stop, and no row is a tab stop where rows are not interactive — so it cannot
be satisfied by making everything focusable. `cursor-move-keeps-focus` asserts the
first `ArrowDown`, because a second press puts focus back on a row by design.

## What is guarded outside this package

Two rules are better asserted where they are *declared* than where they are
rendered, and they live in `web/contract` rather than here:

- **Sessions are read-only, forever** — `contract/src/read-only.ts` is a
  compile-time assertion that no `/sessions*` path declares a `post`/`put`/`patch`/
  `delete` operation, apart from the one exception it names (disconnecting a guest).
  This suite can only observe the *absence* of a write; the contract assertion forbids
  the capability, and fails during `pnpm -r typecheck` with a message naming the path
  and the verb.
- **`PortalContext.terms.url` does not exist** — the reasoning is in the `terms`
  schema description in `openapi.yaml`, so it is not helpfully added.
