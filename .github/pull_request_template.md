## What and why

<!-- What this changes, and the reason. Link the issue if there is one. -->

## How it was verified

<!--
What you ran and what it showed. For a change to a screen: you drove it in a browser
(and, for the guest portal, on a real phone) — attach screenshots. For RADIUS/EAP: the
lab test or eapol_test case. A test added for a bug should fail without the fix.
-->

- [ ] `go build ./... && go vet ./... && go test ./...`
- [ ] `go test -race ./...`
- [ ] `gofmt -l ./internal ./cmd ./lab ./tools` prints nothing
- [ ] `cd web && pnpm -r typecheck && pnpm -r test` (if `web/` changed)
- [ ] `make lab-test` (if FreeRADIUS, the generator, the Policy API or EAP changed)

## Invariants touched

<!-- See docs/rules/ for your area. -->

- [ ] Does not hand-edit generated `raddb/` (changes go in `internal/raddb/`)
- [ ] Does not write `radacct`/`radpostauth` outside the two audited exceptions
- [ ] No credential material crosses the `rlm_rest` boundary
- [ ] No default credential, shared secret or CA added
- [ ] Any new operator setting has a console screen and a database row (no env var, file or CLI step)
- [ ] `CONTRACT.md` updated if a package invariant changed
- [ ] `docs/protocol-reality.md` updated (with MEASURED / DOCUMENTED / UNVERIFIED) if this rests on a new protocol or vendor fact

## Sign-off

- [ ] Every commit carries a `Signed-off-by:` line ([DCO](https://developercertificate.org/), `git commit -s`)
