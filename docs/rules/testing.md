# Testing guide — and what the lab can and cannot prove

> Governs every `*_test.go`, `lab/`, `tools/`, and the test suites under `web/`.

## Tiers

| Tier | What | How to run | Needs hardware? |
|---|---|---|---|
| 1 | Go unit tests, table-driven, PostgreSQL via testcontainers; frontend unit tests in vitest | `go test ./...`, `go test -race ./...`; `cd web && pnpm -r test` | no |
| 2 | Real FreeRADIUS 3.2.x in `lab/`: `radclient` **and `eapol_test`**, `radiusd -XC`, snapshot-tested generated config, blue/green cutover | `make lab-test` | no |
| 3 | Portal and admin end-to-end (Playwright) **against `web/mock`**, with axe over every route in both themes | `cd web && pnpm -F @foyer/e2e test:e2e` ([`web/e2e/README.md`](../../web/e2e/README.md)) | no |
| 4 | Acceptance on real equipment **plus a device matrix**: iOS, Android, Windows, macOS | by hand, before a release | yes |

`make check-all` runs the tier 1 checklist for Go and the frontend; `make ci` adds the
contract differ (`make diff-api`) and tier 2.

Lock behaviour in tiers 1–3 so it cannot silently regress, and **also** drive the
surface: a UI feature is verified when someone has used it in a real browser as the
person it is built for — not when an API call returned 200. An API-level check never
replaces driving the screen.

### Tier 1 in `internal/store`: one container, one database per test

`internal/store`'s `TestMain` starts one PostgreSQL and migrates **`foyer_template`**
once. The first harness helper a test calls (`appStore`, `storeAsRole`,
`ownerPool`, `ownerURL(t)`, `appURL(t)`, `radiusURL(t)`, `productStore`,
`radiusConn`) clones it with `CREATE DATABASE t_<n> TEMPLATE foyer_template`, and
the clone is dropped in `t.Cleanup`. The world is keyed by the **top-level** test,
so subtests share their parent's database.

- **Do not add a fixed-value row to a shared database** to save the clone. Many store
  tests seed fixed addresses and codes on purpose, because the value is what the test
  is about; in a shared database they pass once and fail under `-count=2`.
- **Roles and their passwords are cluster-wide** and survive cloning — the logins
  `TestMain` provisions work in every world, and a test that changes a role's
  password changes it for all of them. `ALTER DATABASE … SET` and database ACLs are
  *not* copied; no migration uses either. If one starts to, it will pass in
  production and fail in the clone.
- `freshDatabase` is the other kind: empty and unmigrated, for tests about applying
  migrations. `internal/apistore` shares one database and stays isolated by giving
  every `world` its own address and MAC space (`worldSeq`).

## eapol_test is not optional

`radclient` and `radtest` **do not speak EAP**. A project whose architectural
rationale is "EAP is too hard to implement ourselves" cannot have zero EAP coverage
in CI.

`eapol_test` (from wpa_supplicant) links an EAP peer and a RADIUS client into one
binary specifically to test EAP methods without an access point, and is designed
to be scripted. Every EAP method Foyer claims to support has an `eapol_test`
case in [`lab/eap`](../../lab/eap/README.md), and the claim is removed if the case is
removed.

A lab database that fails to migrate after a pull has usually been migrated by a
tree whose migrations are numbered differently. Do not repair it by hand: `make lab-reset`
([`lab/README.md`](../../lab/README.md)).

## What the lab cannot do — state this honestly

**`lab/` prevents regressions in what has already been learned from hardware. It
cannot teach what hardware does.** A hand-written NAS mock encodes the author's
assumptions — the same assumptions that produce the bug.

- **ACKing a CoA is trivial; faithfully *rejecting* one is not.** Real gear NAKs
  on session-identification mismatches, differs per vendor, is picky about source
  IP, sometimes listens on 1700, and returns `Error-Cause` codes you must handle.
- **Playwright cannot emulate the iOS Captive Network Assistant.** The portal
  suite can be green and prove nothing about real phones — the
  Cancel-instead-of-Done failure is invisible to it. That is why tier 4 is a
  device matrix and not "one AP", and why testing the portal on a real phone is
  not substitutable.
- Fixtures for the UniFi controller are pinned to a controller version. When they
  drift, the failure must be **loud** — a capability probe at NAS registration,
  never a silent degradation.

When real equipment teaches something new, the deliverable is a **new lab test plus
a line in [`docs/protocol-reality.md`](../protocol-reality.md)** — not a fix alone.

## Named security tests

Generic "add tests for validators" is not enough for an authentication system. These
exist by name and must not be deleted without replacing them:

- **Credential-storage invariants** — no argon2-hashed identity is ever reachable
  by MSCHAPv2; no NT hash lives in the general accounts table
  (`internal/store/credentials_test.go`, `lab/policy/credentials_test.go`).
- **`require_message_authenticator` snapshot** — the generated configuration
  contains it everywhere it must (BlastRADIUS, [`security.md`](security.md) §3;
  `TestRequireMessageAuthenticatorSnapshot`).
- **Inner/outer identity** — a mismatched outer identity does not influence the
  authorization decision (`internal/aaa/identity_test.go`).
- **Garden escape** — a pre-auth client cannot reach anything outside the
  configured walled garden, including via DNS. Enforcement is the NAS's, so this is a
  tier 4 check on real equipment; tier 1 pins only that required garden entries
  cannot be deleted.
- **`VerifyPresence`** — no grant is created for a MAC the driver did not confirm
  present (`TestVerifyPresence_NoGrantForAMACTheDriverDidNotConfirm_OverHTTP`).
- **Policy API unavailability** — SIGSTOP the Go process; assert the *defined*
  per-site behaviour (fail-closed for 802.1X). This test is the reason the
  `rlm_rest` architecture is defensible (`TestSIGSTOPOnThePolicyAPI`,
  [`lab/policy`](../../lab/policy/README.md)).
- Injection payloads on every input validator.

## Named frontend tests

Same standing. Each guards a failure that is invisible in a browser or fatal on
hardware. Specs live under `web/e2e/tests/`.

- **`navigation-not-xhr`** — the login is a document navigation answered `303`;
  `fetch`/`XMLHttpRequest`/`sendBeacon` are wrapped from an init script that survives
  MPA navigations, so a build that signs in by XHR (and disassociates every iPhone)
  fails.
- **Zero external requests** — the built portal contains no absolute URL to another
  origin.
- **Terms before authorization** — nothing the guest must see is rendered after
  the grant call, because Android destroys the WebView.
- **Capability gating** — expected control counts are computed from the NAS's own
  capability model, so a gate that diverges from the model fails loudly.
- **The fixtures are still ugly** — the pathological rows survive many mock ticks
  unchanged; a world that heals itself removes the cases the design is built against.
- **`responsive-shell`** — `document.scrollWidth` equals the layout viewport on every
  admin route at 390 px, and a named case re-injects the overflowing widths and
  requires every route to fail.
- **`narrow-values-reachable`** — the columns a narrow table drops are *readable*, not
  merely present, measured against `.table-scroll`'s `clientWidth`.
- **`drawer-focus`** — the hand-written admin drawer traps focus, closes on `Escape`
  and returns focus.
- **`touch-actions-visible`** — under `(hover: none) and (pointer: coarse)` identifier
  actions are opaque and hittable, asserted on computed `opacity`.

### Focus and keyboard

Zero axe violations covers only part of an accessibility audit. These carry the rest,
and most of them re-inject the defect from the page so the evidence that they can fail
is re-derived on every run:

- **`dialog-focus-return`** — every overlay hands focus back to the control that
  opened it, on `Escape` and on its own cancel control.
- **`confirm-dialog-focus-return`** — the same for confirmation dialogs, whether the
  call site mounts them always or conditionally.
- **`dialogs-are-named`** — every `dialog[open]` resolves an accessible name.
- **`shortcuts-through-a-modal`** — a navigation shortcut with an overlay
  open does nothing; the overlay list is driven, never a hardcoded count.
- **`palette-is-an-overlay`** — the command palette is modal: focus is trapped and the
  page behind is inert.
- **`row-activation`** — `Enter` and `Space` do what a click does on tables whose rows
  are a tab stop, **and** no row is a tab stop on tables whose rows are not.
- **`cursor-move-keeps-focus`** — `ArrowDown` leaves focus on a row; `Enter` moves it
  into the dock.
- **`row-count-announced`** — the live region names the new count after a filter and
  is silent when a refetch changes nothing.
- **`pending-does-not-eat-focus`** — a control that is pending uses `aria-disabled`
  and a guarded handler, never `disabled`, so focus is not blurred to `<body>`.
- **`destructive-action-restores-focus`** — after a confirmed delete, focus moves to
  the first action on the row that took its place.

The narrow-viewport specs are the only coverage of the admin's narrow rendering: the
admin's unit tests use `renderToStaticMarkup`, where the narrow hook's server snapshot
is `false` by design.

Two CI gates hard-fail rather than warn: the portal byte budget
(`web/tools/check-budget.mjs`) and the colour-token lint (`web/tools/check-tokens.mjs`).

## Principles

Each of these is a rule because a green suite is not evidence that the thing works.

- **A test must be seen failing.** When adding one for a bug, confirm it fails against
  the unfixed code — and read *why* it failed: a sabotage that stops the package
  compiling exits non-zero and proves nothing about the assertion.
- **Check the exit status of what you ran.** `go test` exits 0 for a package with no
  test files, for one whose tests all skip, and for `-run` patterns that match
  nothing; a pipeline ending in `| tail` reports the status of `tail`. Gates that
  must have run something require a `--- PASS:` line.
- **Derive stand-ins from the authority.** A fixture, fake driver or seeded row is an
  input the harness cannot be wrong about, and it produces confident output in either
  direction. Do not keep a stale second copy of a rule whose authority is available —
  read the predicate, the database constraint or the driver's capability set instead.
  A fixture world that cannot express a property (for example, one with a single
  site) makes every check of that property vacuous.
- **Assert where the subject appears.** An assertion aimed where the subject does not
  appear is green both ways: if repairing the defect does not change the result, it is
  aimed at the wrong surface (text in a `title` attribute is
  not in `toContainText`). Lock both branches so a fix cannot later be a deletion.
- **Bound polling assertions on both sides.** A single sample of a DOM attribute taken
  at the moment a request is observed is a race by construction; wait, bounded. And
  ask what else could make a poll pass within its window — if the answer is "waiting
  long enough", the assertion is about time, not the mechanism. Isolated-green and
  in-suite-red is a timing signature, not a flaky test to rerun.
- **A guard that matches a name must also check the value.** "No credential-shaped
  field" is "no credential-shaped field carrying a credential-shaped value"; a boolean
  is never a credential. Do not fix a false positive with an allow-list of names.
- **A diagnostic keeps the output that discriminates.** A tool that discards stderr
  and treats an empty answer as "down" reports outages that are not there.
- **A green suite is a claim about a tree and a build state.** `internal/webui`'s
  embedded frontend is a build step outside `go test`, so state whether the frontend
  was embedded; a test whose subject differs between the two states takes the state
  as an argument and drives both. The state to prefer is the one a fresh clone gets.
- **Know which servers the E2E suite attached to.** `reuseExistingServer` attaches to
  whatever already holds the suite's ports ([`web/e2e/README.md`](../../web/e2e/README.md)).

### A skip whose reason expires

A skip is coverage that removed itself and left a note explaining why; when the note
stops being true, repairing the product turns nothing red. **A skip must name a
condition a test can evaluate, not a phase or a milestone.** *"No voucher in this
world is `unused` in the site this guest is standing in"* is evaluated on every run;
a milestone name never is.

`tools/codegen/differ/skips.go` refuses, before either server starts, a skip whose
reason cannot be evaluated and an operation that is neither driven nor skipped.
Every conditional step in the differ scenario states its condition in an `else`, and
`TestTheRealScenarioPassesItsOwnSkipRules` runs the shipped scenario through the
validator.

An evaluable condition that guards a build step (`if ls some/package/*.go; then …`)
must be deleted by the change that resolves its uncertainty: left in place, a rename
turns it false again and the build exits 0 having checked nothing.

## Discipline

- Never mark a test skipped to make CI green. Either fix it or delete it with a
  stated reason in the commit message.
- A test that has never failed is not necessarily proving anything — when adding
  one for a bug, confirm it fails against the unfixed code first.
- Snapshot tests on generated config are the main defence against the highest
  risk in the project: generated configuration that is subtly wrong and fails only on
  hardware. Review snapshot diffs as carefully as code.
- **A feature is verified when someone drove it**, not when a request returned 200.
  `ssh`, `curl`, `psql` and `docker exec` are diagnostics and never the fix: if a
  manual step is what makes something work, that is a defect in the repository, and
  the repository is changed so that it does the step.
