# internal/webui — CONTRACT

The two frontends, embedded in the Go binary that already answers their API calls.
No nginx, no second container, no second origin.

## What it exports

```go
func Built() bool              // false when this binary carries no frontend
func Portal() http.Handler     // the guest portal's documents
func PortalHas(urlPath string) bool
func PortalLanguages() []string
func Admin() http.Handler      // the console, single-page
```

## Invariants

1. **`Built()` is the gate, and a caller must check it before composing anything.**
   When it is false, `PortalHas` answers false for **every** path and both handlers
   answer `503` on every path. A caller that used `PortalHas` to choose between this
   package and `portal.Service.EntryHandler`, and fell back to the entry redirect on
   false, would **recreate the infinite redirect loop this package exists to prevent**.
   Mount `Portal()` as the catch-all, or refuse to start.

   **Every exported symbol honours this gate**, including `PortalLanguages()`: a
   half-copied binary that advertised languages for documents answering `503` would
   send a caller negotiating against a set it cannot serve.

2. **`/` is never a portal route.** `web/portal/dist/index.html` is the build's
   static best-effort root — a `<meta http-equiv="refresh">` that necessarily drops
   the query string, and with it the `id` and `ap` that `VerifyPresence` is the only
   evidence for. `build.ts` says so in place and names the Go binary as its
   replacement. `newPortalTree` deletes the route, so no arrangement of a caller's
   mux can let the stub win. `/` belongs to `EntryHandler`.

3. **The portal is exact-match, and never redirects to `/` or to itself.** A path
   that is not a document is a `404`. That is what makes the handler safe as a
   catch-all — `/api/v0/…` and `/.well-known/captive-portal` cannot become documents
   — and it is what forbids a redirect loop directly.

   **Rendered documents answer the same paths.** `GuestDocumentsHandler` in
   `internal/api` renders `/{lang}/…` per request from the venue's settings;
   the static tree stays the fallback beneath it. Same paths, same `no-store`,
   same 404 for what is not a document — a mistyped URL is a 404 on both, never
   a venue page. The static tree is what a binary with no settings port still
   serves; the renderer is what a configured venue's guests read.
4. **A canonical redirect relays the query string verbatim.** `/en/voucher` →
   `/en/voucher/?<query unchanged>`, `302`, `Cache-Control: no-store`. Relayed, never
   parsed: these parameters are unsigned and attacker-controlled and stay untrusted
   at the far end (`docs/rules/security.md` §4).

5. **Portal documents are `no-store`.** Android calls `clearCache(true)` on every
   portal launch and the CNA's data store is ephemeral, so a validator buys nothing
   on the two clients that matter. What a cache *can* still do is hold a stale
   document in front of a repaired portal.

6. **The admin's SPA fallback never swallows `/api/…` or a miss under `/assets/`.**
   The first would reach the console as a JSON parse error; the second as a syntax
   error inside a page, hiding the half-copied tree that caused it. Both are `404`.

7. **No request path reaches a filesystem.** The cleaned path is a **map key** and
   nothing else — there is no `Open(userInput)` in this package — so traversal is
   unrepresentable rather than defended against. Segments beginning `.` or `_` are
   refused twice: they never become routes, and they are refused again at request
   time.

8. **`mediaType` is an explicit table, never `mime.TypeByExtension`.** That function
   reads the build machine's `/etc/mime.types`, so the `Content-Type` on a shipped
   artefact would depend on who built it.

## The compile-time problem, and how it is solved

`//go:embed` fails to **compile** when its directory is missing. Left alone that
breaks `go build ./...` on every fresh clone and in every CI job that does not run
`pnpm build`.

So `portal-dist/` and `admin-dist/` each carry one committed file — `.gitkeep`, the
only tracked file in either — and the directive is `//go:embed all:`, because a bare
`//go:embed` refuses a directory whose only file is a dotfile. `all:` is right on its
own merits too: the portal's build is a custom script, not Vite, so a future dotfile
would be silently dropped by the bare form.

**`Built()` is derived from the artefacts, never from the placeholder.** The
placeholder is present on a workstation and absent in an image (the Dockerfile
excludes both directories from the build context), with no bearing on whether a
document exists in either. So it asks the question a caller actually has: is there a
language landing under `portal-dist`, and does `admin-dist/index.html` exist **with
every asset it references**.

## What `Built() == false` causes

- `Portal()` and `Admin()` answer **`503`** on every path — never a `200`, never a
  blank page — with `X-Foyer-UI: absent`, `Cache-Control: no-store`, and a body
  naming the build step and, for a half-copied console tree, the exact files
  `index.html` asks for that are not embedded.
- `PortalHas` answers false everywhere. See invariant 1.
- `go test ./internal/webui/` **skips** the four artefact assertions on a workstation
  that has not run `pnpm build`, and **fails** them when `FOYER_REQUIRE_WEBUI` is
  set. The skip's reason is a condition a test can evaluate and that expires by
  itself, which is what `docs/rules/testing.md` requires of one.

  **`FOYER_REQUIRE_WEBUI` is the only trigger.** It is set where the embedded
  frontend is actually built — `make check-all` (after the byte budgets) and both
  Dockerfiles. Keying the gate on a generic `CI` variable as well would fail every CI
  job that does not run `make web-embed`, and a gate that is red for reasons unrelated
  to what it guards is one people learn to ignore.

This follows `internal/version`: a build that cannot name itself must say so, because
*the silence is the failure mode*.

## The division of labour with the build graph, and neither half is sufficient

Two failure modes sit either side of the embed directories, and each is invisible from
the other side.

| Failure | Who catches it | How |
|---|---|---|
| **A reference with no file** — `index.html` names `/assets/index-<hash>.js` and the file is not embedded | **this package** | `adminTree.built()` parses the document's own `src`/`href` attributes and requires every one to resolve. `Built()` is false, the 503 body names the missing files |
| **A file with no reference** — a stale content-hashed asset left behind by a copy that did not delete | **the build graph** | `make web-embed` deletes before it copies (`rsync -a --delete --exclude '.gitkeep'`, or `find … ! -name .gitkeep -delete`) |
| **A partially copied tree** — one frontend present, the other broken | **created there, describable only here** | `Built()` is false while `thePortal` stays populated. Neither side can find this alone: a package test cannot build a broken image, and the build graph cannot see inside `Built()` |

**Nothing here can see a leftover**, because a stale asset is a file the route table
serves happily and no document names. **Nothing there can see the inverse**, because a
sync that copies a complete `dist` is correct by its own lights even when the `dist`
it was handed is a document with no application behind it — which is why
`adminTree.built()` checks every reference rather than only that `index.html` exists.

The `.gitkeep` in each directory is what makes the delete-sync survivable: it is the
only tracked file in either, and a plain `rm -rf` of the directory dirties the tree
rather than cleaning it.

### A weak red and a weak green are different questions about one exit code

- **A weak red.** A sabotage that stops the package *compiling* exits non-zero
  whether or not the assertion can see the defect. Every sabotage must leave the
  package building, and the `--- FAIL` line is what to read.
- **A weak green.** `go test ./internal/webui/` exits **0** for a package with no test
  files, printing `? … [no test files]`. So a gate that is only `go test` reports
  success for a run in which nothing was asserted. The build graph's three copies of
  the gate require at least one `--- PASS:` line.

The three assertions here that census the real tree — the portal document sweep, the
console's asset references, the extension table — would each pass vacuously if their
walk reached nothing, so each carries a `len(…) == 0` refusal.

### The two mechanisms are complementary

Neither the build graph's `--- PASS:` floor nor this package's `FOYER_REQUIRE_WEBUI`
is redundant, because each is blind exactly where the other sees:

| State of the tree | `go test` exit | Caught by |
|---|---|---|
| Fully built | 0 | — (correct) |
| Half copied | **1** | `FOYER_REQUIRE_WEBUI`, here |
| Nothing embedded | **1** with the variable, `0` without | `FOYER_REQUIRE_WEBUI`, here |
| **No test file at all** | **0** | the `--- PASS:` floor, in the build graph |

An unbuilt run still emits `--- PASS:` lines, because the behavioural tests run
against synthetic trees and do not skip. **So the floor alone cannot see an empty
tree**, and `FOYER_REQUIRE_WEBUI` alone cannot see a deleted suite.

### A half-copied tree is a state each side can see only half of

A package test **cannot build a broken image**: emptying `portal-dist` does not reach
the half state, because `languagesOf` on an empty tree is empty and the defect
disappears with the world that would show it. The build graph **can produce the state
trivially** and cannot see what it does to `languagesOf`, `Built()`, or a failure
message's wording. So the coverage is built where the two meet: the tests take
built-ness and the trees as independent inputs.

- **`TestNoExportAdvertisesWhatAHalfCopiedBinaryCannotServe`** drives a populated
  portal tree with `built` false and requires every export, `PortalLanguages()`
  included, to report "no UI".
- **`TestTheDiagnosisNamesTheStateItIsIn`** holds the diagnosis. In a half-copied
  binary this is the only assertion that fires, so its message is what lands in the
  deploy build's output; a message naming both directories as empty when one is full
  would send the reader to re-run a frontend build that works. `describe()` is the
  single describer every surface reads (the `503` body, the release gate, the
  exported-surface test); `remedy()` is separate because **an empty tree and a
  half-copied one have different causes and different fixes**. The test asserts what
  each state must say **and what it must not**.

## The hazard this package cannot close on its own

`EntryHandler` negotiates `Accept-Language` against the **site's advertised**
languages, which come from the database. The documents come from the frontend build.
Nothing makes those two sets agree. A site advertising a language the build does not
carry gets a `404` — visible, and never a loop — but still a venue nobody can get
online at.

`PortalLanguages()` exists so the composition point can intersect and avoid it. That
is a caller's job; this package only guarantees the `404`.
