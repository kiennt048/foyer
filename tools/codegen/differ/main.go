// Command differ drives one scenario into `web/mock` and into the Go server and
// compares the answers.
//
// `web/mock/src/server.ts` is 108 kB of stateful, schema-validated behaviour
// that the frontends are built against. It is the behavioural oracle for
// `internal/api`, and re-deriving that behaviour by reading screens would throw
// it away.
//
//	make diff-api                                              # the adapter mode
//	go run ./tools/codegen/differ -mock http://…/api/v0 -mode memory
//
// **The contract wins on every disagreement.** A difference the scenario has not
// declared fails the run; a declared one is printed as a known divergence of
// `web/mock`, never as a loosened assertion. The exit code is non-zero on any
// undeclared difference, and on a declared one that has stopped happening.
//
// ── two modes, and why the default is the second ─────────────────────────────
//
// `memory` builds the Go server over `internal/api/memory` and seeds that fake
// with the world it read out of the mock. It proves the API layer answers what
// the oracle answers: routing, the gates, pagination, filtering, site scoping,
// ordering, envelopes and refusals.
//
// **It proves nothing about `internal/apistore`.** This mode never touches the
// adapter, so its report is byte-identical with or without it — run alone, a
// harness that exists to prove the Go server reproduces the oracle would never
// compare the wiring an operator actually gets.
//
// `apistore` is that comparison: PostgreSQL in a container, migrated with goose,
// seeded from the same world, and served through the adapter behind the same
// `internal/api` server. It is the default, because a mode that cannot fail is
// not the one to run by habit.
//
// **There is no `both`.** The scenario ends in mutations and the mock is stateful,
// so a second pass would drive them into a world the first pass had already moved
// — and every later difference would be that, rather than a finding. Run the two
// modes against two freshly started mocks if you want both.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
)

// The two modes.
const (
	modeMemory  = "memory"
	modeAdapter = "apistore"
)

// dirtyWarning is set when -dirty-mock-ok let a mutated mock through. It is
// printed at the top of the run and again beside the verdict, because the top of a
// 400-line report is not where anybody reads the result.
var dirtyWarning string

// harnessPassword is what both servers are driven with.
//
// It is a harness constant and not a credential: nothing in the repository is
// configured with it and nothing deployed accepts it. The Go side is seeded with
// it, and **the mock has to be started with `FOYER_MOCK_ADMIN_PASSWORD` set to
// it** — the mock verifies the sign-in password, generating one per process the
// way first boot does and printing it in a banner no scripted driver can read.
//
// This value opens it only when the two are pinned together, which is why every
// "start it with" line this command prints carries the variable.
const harnessPassword = "harness-only-not-a-deployment-value"

func main() {
	mockBase := flag.String("mock", "http://127.0.0.1:5170/api/v0", "base URL of the running web/mock")
	verbose := flag.Bool("v", false, "print every step, not only the differences")
	mode := flag.String("mode", modeAdapter,
		"which Go server to compare: `apistore` (internal/apistore over a real PostgreSQL) or `memory` (the in-memory fake)")
	sabotage := flag.String("sabotage", "",
		"break one adapter binding on purpose and require the run to go red: "+strings.Join(sabotageNames(), ", "))
	dirtyOK := flag.Bool("dirty-mock-ok", false,
		"run against a mock this scenario has already mutated, and read every difference with that caveat")
	flag.Parse()

	if err := run(*mockBase, *verbose, *mode, *sabotage, *dirtyOK); err != nil {
		fmt.Fprintf(os.Stderr, "\n  differ: %v\n\n", err)
		os.Exit(1)
	}
}

func run(mockBase string, verbose bool, mode, sabotage string, dirtyOK bool) error {
	if mode != modeMemory && mode != modeAdapter {
		return fmt.Errorf("unknown -mode %q: use %q or %q", mode, modeAdapter, modeMemory)
	}
	if sabotage != "" && mode != modeAdapter {
		return fmt.Errorf("-sabotage only means anything in -mode %s: it breaks an adapter binding", modeAdapter)
	}

	mock := &client{base: strings.TrimRight(mockBase, "/"), http: harnessHTTPClient(&http.Client{Timeout: 20 * time.Second})}
	if err := mock.reachable(); err != nil {
		return fmt.Errorf("cannot reach the mock at %s: %w\n"+
			"  start it with:  cd web && PORT=5170 FOYER_MOCK_LIVE=0 \\\n"+
			"                    FOYER_MOCK_ADMIN_PASSWORD=%s pnpm -F @foyer/mock start",
			mockBase, err, harnessPassword)
	}

	fmt.Println("  reading the oracle's world…")
	world, err := snapshot(mock)
	if err != nil {
		return err
	}
	if err := seedProbes(mock, world); err != nil {
		return err
	}
	// Re-read after the probes: `probeNas` writes back what it measured, so the
	// rows the scenario compares must be the post-probe ones on both sides.
	world2, err := snapshot(mock)
	if err != nil {
		return err
	}
	world2.Capability, world2.NasProbes = world.Capability, world.NasProbes
	world = world2

	if err := requireAdmin(world); err != nil {
		return err
	}
	// **A mock this scenario has already driven cannot answer the question.** The
	// script ends in mutations, so a second pass compares against a world the first
	// one moved — and the differences that produces name real fields and read
	// exactly like findings. Refusing is the honest answer: a red report that means
	// nothing is the same disease as a green one that means nothing.
	dirt := mockAlreadyDriven(world)
	if len(dirt) > 0 && !dirtyOK {
		return fmt.Errorf("this mock has already been driven by this scenario — it still holds %s.\n"+
			"  A second pass rotates a secret and revokes a voucher again, and the drift from the\n"+
			"  first shows up as `secretLastUsedAt`, voucher-batch counts and `freshness.state`\n"+
			"  differences that name real fields and read exactly like findings. Restart it:\n\n"+
			"    cd web && PORT=… FOYER_MOCK_LIVE=0 \\\n"+
			"      FOYER_MOCK_ADMIN_PASSWORD="+harnessPassword+" pnpm -F @foyer/mock start\n\n"+
			"  `make diff-api` starts and kills its own, so this only arises when driving a\n"+
			"  long-lived one by hand. Pass -dirty-mock-ok to proceed anyway",
			strings.Join(dirt, ", "))
	}
	if len(dirt) > 0 {
		// Direction-free wording on purpose: the same sentence is printed above the
		// report and again beside the verdict, and "below" would be wrong in one of
		// the two places.
		dirtyWarning = fmt.Sprintf("this mock had already been driven by this scenario (%s) and\n"+
			"  -dirty-mock-ok was passed. **Any difference in this report may be the earlier\n"+
			"  run's own rotation or revocation rather than a disagreement between the two\n"+
			"  servers** — and the ones that are name real fields and read exactly like findings.",
			strings.Join(dirt, ", "))
		fmt.Printf("\n  ── driving a mutated mock ──────────────────────────────────\n  %s\n",
			wrap(dirtyWarning, 2))
	}
	fmt.Printf("  read    %d sites · %d controllers · %d nas · %d sessions · %d vouchers · %d accounts · %d operators\n",
		len(world.Sites), len(world.Controllers), len(world.Nas),
		len(world.Sessions), len(world.Vouchers), len(world.Accounts), len(world.Operators))

	// One measurement of the oracle's own world, printed in both modes and whether
	// or not it is zero.
	//
	// It is here rather than only inside the declaration it drives because a
	// declaration prints when it fires, and the number worth seeing is the one that
	// says it has stopped: `0 of 34` is how a reader learns the fixtures were
	// corrected, and a line that only ever appears when something is wrong cannot
	// carry that.
	if bad := macClaimMismatches(world); len(bad) > 0 {
		fmt.Printf("  oracle  %d of %d sessions claim `macIsRandomised` against the address's own\n"+
			"          locally-administered bit (%s …). `internal/store` reads the bit.\n",
			len(bad), len(world.Sessions), bad[0])
	} else {
		fmt.Printf("  oracle  every session's `macIsRandomised` agrees with the address's\n" +
			"          locally-administered bit.\n")
	}

	steps, err := buildScenario(world)
	if err != nil {
		return err
	}
	// **Before either server is started.** A skip is the absence of a comparison,
	// and a run that reported agreement while quietly not comparing an operation
	// for a reason that expired is the failure this check exists against. See
	// skips.go.
	if err := validateSkips(steps); err != nil {
		return err
	}

	var goSrv *client
	var closeGo func()
	var seedNote string

	switch mode {
	case modeMemory:
		fmt.Printf("\n  ── mode: memory ─────────────────────────────────────────────\n")
		fmt.Println("  The Go server is `internal/api` over `internal/api/memory`. This compares")
		fmt.Println("  the API layer and says nothing about `internal/apistore`.")
		store := memory.NewStore(world)
		// **The guest surface, wired.** With `Config.Portal` left zero, `s.portal`
		// would be nil and all five portal handlers would answer `501` — skips that
		// said "not implemented in Go" would be describing the harness rather than
		// the server. See portal.go for why the driver behind it is derived rather
		// than asserted.
		srv, err := api.New(api.Config{
			Ports:  store.Ports(),
			Portal: store.PortalPorts(harnessGuestDriver(harnessGuestAP(world))),
		})
		if err != nil {
			return err
		}
		ts := httptest.NewServer(srv.Handler())
		// The guest surface is a second listener, not a second path. See
		// `client.portalBase`.
		gp := httptest.NewServer(srv.PortalHandler())
		closeGo = func() { gp.Close(); ts.Close() }
		goSrv = &client{
			base:       ts.URL + api.BasePath,
			portalBase: gp.URL + api.BasePath,
			http:       harnessHTTPClient(ts.Client()),
		}

	case modeAdapter:
		fmt.Printf("\n  ── mode: apistore ───────────────────────────────────────────\n")
		fmt.Println("  The Go server is `internal/api` over `internal/apistore` over a real")
		fmt.Println("  PostgreSQL, migrated with goose and seeded from the world above.")
		ctx := context.Background()
		h, err := startPostgres(ctx)
		if err != nil {
			return err
		}
		closeGo = h.Close
		sd := newSeeder(ctx, h, world)
		if err := sd.seed(); err != nil {
			return fmt.Errorf("%w\n"+
				"  The seeding is the harness's own code. A row it cannot place is a finding\n"+
				"  about the schema or about the read model, and belongs in the report rather\n"+
				"  than in a workaround", err)
		}
		seedNote = sd.report()
		fmt.Print(seedNote)
		if err := h.serve(sabotage, world); err != nil {
			return err
		}
		goSrv = h.client()
		if sabotage != "" {
			fmt.Printf("\n  ── sabotage: %s ──\n  %s\n", sabotage, wrap(sabotageReason(sabotage), 2))
		}
	}
	defer closeGo()

	// Both servers are signed in as the same operator before the scenario runs,
	// so that a step which is *about* being signed out has to say so.
	if err := mock.signIn(adminUsername(world)); err != nil {
		// **The likely cause is one env var, so it is named rather than left to
		// be deduced from a 401.** The mock generates its administrator password
		// per process and prints it in a banner; a driver that cannot read the
		// banner has to pin it, and a driver that forgets gets exactly this.
		// That is the failure the pin was designed to produce — the alternative is
		// a mock that signs in an administrator with any string.
		return fmt.Errorf("signing in to the mock: %w\n"+
			"  This mock verifies the password. Start it with\n"+
			"    FOYER_MOCK_ADMIN_PASSWORD=%s\n"+
			"  or export that variable before `make diff-api`, which passes its own\n"+
			"  environment through to the mock it starts", err, harnessPassword)
	}
	if err := goSrv.signIn(adminUsername(world)); err != nil {
		return fmt.Errorf("signing in to the Go server: %w", err)
	}

	res := runScenario(mock, goSrv, steps, mode, verbose)
	err = report(res, steps, mode)
	if sabotage != "" {
		return sabotageVerdict(err)
	}
	return err
}

type outcome struct {
	step step
	// declared maps a declaration index to the differences it accounted for.
	declared map[int][]diff
	// undeclared is every difference no declaration covers. Any one of them
	// fails the run.
	undeclared []diff
	// vanished names declarations that accounted for nothing, which means either
	// the mock was fixed or the Go server drifted towards the defect.
	vanished      []divergence
	orderDiffered bool
	status        string
}

func (o outcome) failed() bool { return len(o.undeclared) > 0 || len(o.vanished) > 0 }

func runScenario(mock, goSrv *client, steps []step, mode string, verbose bool) []outcome {
	var out []outcome
	for _, s := range steps {
		if s.skipped(mode) != "" {
			continue
		}

		mockAnswer, err := mock.drive(s)
		if err != nil {
			out = append(out, outcome{step: s, undeclared: []diff{{Key: keyBody, Text: "mock: " + err.Error()}}})
			continue
		}
		goAnswer, err := goSrv.drive(s)
		if err != nil {
			out = append(out, outcome{step: s, undeclared: []diff{{Key: keyBody, Text: "go: " + err.Error()}}})
			continue
		}

		// **Ordering is relaxed in the adapter mode alone, and reported.** The
		// contract defines no order for any list operation and the two servers
		// genuinely pick different ones; the fake, seeded from the oracle's own
		// slices, happens to agree.
		opts := compareOpts{SortItems: mode == modeAdapter}
		diffs, orderDiffered := compare(mockAnswer, goAnswer, s.volatileKeys(mode), opts)

		o := outcome{
			step:          s,
			orderDiffered: orderDiffered,
			status:        fmt.Sprintf("mock %d / go %d", mockAnswer.Status, goAnswer.Status),
		}
		o.declared, o.undeclared = classify(diffs, s.declarations(mode))
		for i, d := range s.declarations(mode) {
			if len(o.declared[i]) == 0 {
				o.vanished = append(o.vanished, d)
			}
		}
		out = append(out, o)

		if verbose {
			mark := "="
			if len(diffs) > 0 {
				mark = "≠"
			}
			fmt.Printf("    %s %-7s %-58s %s\n", mark, s.Method, s.Path, o.status)
		}
	}
	return out
}

// classify attributes each difference to the first declaration that covers it.
//
// A declaration with no `Keys` covers the whole answer and is checked last, so a
// key-scoped declaration on the same step still gets its own differences and can
// still be found to have vanished.
func classify(diffs []diff, decls []divergence) (map[int][]diff, []diff) {
	declared := map[int][]diff{}
	var undeclared []diff

	scoped := func(d diff) int {
		for i, decl := range decls {
			for _, k := range decl.Keys {
				if k == d.Key {
					return i
				}
			}
		}
		for i, decl := range decls {
			if len(decl.Keys) == 0 {
				return i
			}
		}
		return -1
	}

	for _, d := range diffs {
		if i := scoped(d); i >= 0 {
			declared[i] = append(declared[i], d)
			continue
		}
		undeclared = append(undeclared, d)
	}
	return declared, undeclared
}

func report(outcomes []outcome, steps []step, mode string) error {
	var failures []outcome
	declaredCount, ordered := 0, 0
	for _, o := range outcomes {
		if o.orderDiffered {
			ordered++
		}
		declaredCount += len(o.declared)
		if o.failed() {
			failures = append(failures, o)
		}
	}

	// ── coverage, honestly ───────────────────────────────────────────────
	all := generatedOperations()
	covered := map[string]int{}
	skipped := map[string]string{}
	driven := 0
	for _, s := range steps {
		if reason := s.skipped(mode); reason != "" {
			if _, already := covered[s.Op]; !already {
				skipped[s.Op] = reason
			}
			continue
		}
		covered[s.Op]++
		delete(skipped, s.Op)
		driven++
	}

	fmt.Printf("\n  ── coverage (%s) ────────────────────────────────────────────\n", mode)
	fmt.Printf("  %d of %d operations compared, over %d requests.\n", len(covered), len(all), driven)

	var uncovered []string
	for _, op := range all {
		if covered[op] == 0 {
			uncovered = append(uncovered, op)
		}
	}
	sort.Strings(uncovered)
	if len(uncovered) > 0 {
		fmt.Printf("\n  Not compared (%d) — every one with its reason, because a silent gap\n"+
			"  reads as \"covered everything\":\n", len(uncovered))
		for _, op := range uncovered {
			reason := skipped[op]
			if reason == "" {
				reason = "NO REASON RECORDED — add one to the scenario"
			}
			fmt.Printf("    · %-24s %s\n", op, reason)
		}
	}

	// ── what this mode relaxed, printed rather than applied silently ─────
	if mode == modeAdapter && ordered > 0 {
		fmt.Printf("\n  ── list ordering ───────────────────────────────────────────\n")
		fmt.Printf("  %d list answers were compared as sets rather than as sequences, because the\n"+
			"  two servers returned the same rows in different orders. **The contract defines\n"+
			"  no order for any list operation** — the oracle answers in fixture order and\n"+
			"  `internal/store` orders by id or by `created_at DESC` — so neither is wrong and\n"+
			"  a console paging through either gets a different first page. Known contract\n"+
			"  gap: `Page` wants to say what the order is.\n", ordered)
	}

	// ── findings against the oracle ──────────────────────────────────────
	if declaredCount > 0 {
		fmt.Printf("\n  ── declared divergences, known ─────────────────────────────\n")
		seen := map[string]bool{}
		for _, o := range outcomes {
			decls := o.step.declarations(mode)
			for i, d := range decls {
				if len(o.declared[i]) == 0 || seen[d.Reason] {
					continue
				}
				seen[d.Reason] = true
				scope := "the whole answer"
				if len(d.Keys) > 0 {
					scope = "`" + strings.Join(d.Keys, "`, `") + "`"
				}
				fmt.Printf("\n  %s %s  (%s)\n    covers %s\n", o.step.Method, o.step.Path, o.status, scope)
				fmt.Printf("    %s\n", wrap(d.Reason, 4))
				// **What it actually covered, not only what it claims to.** A
				// declaration that printed its reason and hid its differences would
				// be a check that looks like one and is not: a green line standing
				// in for a measurement nobody sees. The figures
				// are what tell a reader whether the declaration still describes the
				// difference in front of it.
				printSample(o.declared[i])
			}
		}
	}

	// ── failures ─────────────────────────────────────────────────────────
	vanished, undeclared := 0, 0
	for _, o := range failures {
		if len(o.vanished) > 0 {
			if vanished == 0 {
				fmt.Printf("\n  ── declared divergences that did not happen ────────────────\n")
				fmt.Printf("  A divergence that stops happening is not good news by default: either\n" +
					"  the oracle was corrected — remove the declaration — or the Go server has\n" +
					"  drifted towards the defect.\n")
			}
			for _, d := range o.vanished {
				vanished++
				fmt.Printf("    %s %s (%s)\n      %s\n", o.step.Method, o.step.Path, o.status, d.Reason)
			}
		}
	}
	for _, o := range failures {
		if len(o.undeclared) == 0 {
			continue
		}
		if undeclared == 0 {
			fmt.Printf("\n  ── undeclared differences ─────────────────────────────────\n")
		}
		fmt.Printf("\n  %s %s  (%s)\n", o.step.Method, o.step.Path, o.status)
		for _, d := range o.undeclared {
			undeclared++
			fmt.Printf("      %s\n", d.Text)
		}
	}

	if len(nullsDropped) > 0 {
		var names []string
		total := 0
		for k, n := range nullsDropped {
			names = append(names, k)
			total += n
		}
		sort.Strings(names)
		fmt.Printf("\n  ── null vs absent ──────────────────────────────────────────\n")
		fmt.Printf("  %d occurrences across %d field names were treated as equal: the mock\n"+
			"  writes an explicit null where oapi-codegen omits an optional nullable\n"+
			"  property. Both conform; the equivalence is printed rather than applied\n"+
			"  silently, so a name in this list that ought to be required is visible.\n", total, len(names))
		fmt.Printf("    %s\n", wrap(strings.Join(names, ", "), 4))
	}

	if dirtyWarning != "" {
		fmt.Printf("\n  ── read the above with this caveat ─────────────────────────\n  %s\n",
			wrap(dirtyWarning, 2))
	}

	fmt.Println()
	if undeclared > 0 || vanished > 0 {
		return fmt.Errorf("%d undeclared difference(s), %d declared divergence(s) that did not happen",
			undeclared, vanished)
	}
	fmt.Printf("  agreed on every compared step (%d declared divergences).\n\n", declaredCount)
	return nil
}

// generatedOperations reads the operation names off the generated interface, so
// the denominator of the coverage figure cannot fall behind the contract.
func generatedOperations() []string {
	iface := reflect.TypeOf((*gen.StrictServerInterface)(nil)).Elem()
	out := make([]string, 0, iface.NumMethod())
	for i := 0; i < iface.NumMethod(); i++ {
		out = append(out, iface.Method(i).Name)
	}
	sort.Strings(out)
	return out
}

// printSample prints the first few differences a declaration accounted for, and
// says how many more there were. All of them at 625 vouchers would bury the report;
// none of them would make a declaration unfalsifiable by eye.
func printSample(diffs []diff) {
	const show = 4
	for i, d := range diffs {
		if i == show {
			fmt.Printf("      … and %d more like it\n", len(diffs)-show)
			break
		}
		fmt.Printf("      %s\n", d.Text)
	}
}

func wrap(s string, indent int) string {
	const width = 74
	var b strings.Builder
	col := 0
	for _, word := range strings.Fields(s) {
		if col+len(word)+1 > width {
			b.WriteString("\n" + strings.Repeat(" ", indent))
			col = 0
		} else if col > 0 {
			b.WriteString(" ")
			col++
		}
		b.WriteString(word)
		col += len(word)
	}
	return b.String()
}

// portalCookieName is the guest session cookie both servers set.
//
// Not derived from either implementation, and it cannot be: the name is a wire
// value the two agree on by having read the same contract, and the harness's job
// is to notice when they stop. If one side renames it, this constant stops
// matching and every portal step after the first reports a fresh session — which
// is a loud failure rather than a quiet one.
const portalCookieName = "foyer_portal_session"

// formValues turns a step body into `application/x-www-form-urlencoded`.
//
// It accepts the same `map[string]string` the JSON path takes, so a step reads
// the same way whichever encoding it declares.
func formValues(body any) (url.Values, error) {
	m, ok := body.(map[string]string)
	if !ok {
		return nil, fmt.Errorf("a form-encoded step needs a map[string]string body, got %T", body)
	}
	out := url.Values{}
	for k, v := range m {
		out.Set(k, v)
	}
	return out, nil
}

// ── the two clients ──────────────────────────────────────────────────────────

type client struct {
	base string
	// portalBase is where the guest surface answers, which is **not always the
	// same listener**.
	//
	// `web/mock` serves both surfaces from one Hono app on one port. The Go
	// server does not: `internal/api` splits `Handler()` from `PortalHandler()`
	// and refuses a console operation on the guest listener with `404` rather
	// than `401`, because a refusal that says the route exists is the wrong
	// answer on the listener bound where anyone within radio range can reach it
	// (`docs/rules/security.md` §7 — admin UI and Policy API on separate
	// listeners from the guest portal).
	//
	// **That split is itself a finding about the oracle**, and it is a
	// structural one rather than a field: the mock cannot express it, so nothing
	// driven through the mock can show it working. It is recorded here rather
	// than as a step divergence because a step cannot compare two listeners.
	portalBase string
	http       *http.Client
	// cookie is the console's session. `guest` is the portal's, and they are
	// two jars rather than one because they are two surfaces: the console's
	// carries the `__Host-` prefix and a CSRF token, the portal's carries
	// neither and is set pre-authentication by anyone within radio range.
	cookie *http.Cookie
	guest  *http.Cookie
	csrf   string
}

// harnessHTTPClient is the transport both servers are driven with.
//
// **`CheckRedirect` is the load-bearing line.** Go's default client follows a
// `303`, so the answer this harness compared was the *destination* — and the
// destination of a portal redirect is `web/portal`'s dev server, which is not
// running. `compare.go`'s `Location` comparison, written specifically for the
// portal's `303`s, could therefore never see one: it compared two empty strings
// on every step and was vacuous by construction rather than by accident.
//
// `ErrUseLastResponse` stops at the redirect and hands back the `303` itself,
// which is the response the contract describes and the one whose `Location`
// decides whether an iPhone stays on the network.
func harnessHTTPClient(base *http.Client) *http.Client {
	c := *base
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if c.Timeout == 0 {
		c.Timeout = 20 * time.Second
	}
	return &c
}

func (c *client) reachable() error {
	resp, err := c.http.Get(c.base + "/sites")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (c *client) signIn(username string) error {
	a, err := c.do(http.MethodPost, "/auth/session", map[string]string{
		"username": username, "password": harnessPassword,
	}, false, false)
	if err != nil {
		return err
	}
	if a.Status != http.StatusOK {
		return fmt.Errorf("%d %s", a.Status, truncate(a.Body))
	}
	var out gen.AuthSession
	if err := json.Unmarshal(a.Body, &out); err != nil {
		return err
	}
	c.csrf = out.CsrfToken
	if c.cookie == nil {
		return fmt.Errorf("no session cookie set")
	}
	return nil
}

func (c *client) drive(s step) (answer, error) {
	saved := c.cookie
	savedCSRF := c.csrf
	switch {
	case s.Guest:
		// **The console's session must not reach the guest surface.** The five
		// portal operations are `x-permission: public` and declare `security: []`,
		// so a request carrying an administrator's cookie would be answered by a
		// server that had been handed one — and the thing under comparison is what
		// a phone gets. The portal's own cookie travels instead, in `c.guest`.
		c.cookie, c.csrf = nil, ""
	case s.Anonymous:
		c.cookie, c.csrf = nil, ""
	case s.NoCSRF:
		c.csrf = ""
	}
	defer func() { c.cookie, c.csrf = saved, savedCSRF }()

	if s.Collect {
		return c.collect(s.Path)
	}
	return c.do(s.Method, s.Path, s.Body, s.Form, s.Guest)
}

// collect follows `nextCursor` to the end and answers with the whole collection.
//
// **A page is not comparable between two servers that order differently.** The
// contract defines no order, the oracle answers in fixture order and
// `internal/store` orders by id or by `created_at DESC`, so page one of a 625-code
// list is a different 200 codes on each side and every row lines up against the
// wrong one. Following the cursor turns the question back into the one worth asking
// — *do the two servers hold the same rows, with the same values* — and leaves the
// ordering difference to be reported once rather than 200 times.
//
// The cursor itself is never compared: the two encodings are different by
// construction (a decimal offset over a slice, against a repository keyset) and the
// contract types it `string` and says nothing more. The collected answer carries
// `nextCursor: null`, which is what it now truthfully is.
func (c *client) collect(path string) (answer, error) {
	u, err := url.Parse(path)
	if err != nil {
		return answer{}, err
	}
	q := u.Query()
	q.Set("limit", "200")

	var items []json.RawMessage
	var envelope map[string]json.RawMessage
	var out answer
	cursor := ""
	// A server that answers with the cursor it was given would loop forever, and
	// a harness that hung would be indistinguishable from a slow one.
	for pages := 0; pages < 64; pages++ {
		if cursor == "" {
			q.Del("cursor")
		} else {
			q.Set("cursor", cursor)
		}
		u.RawQuery = q.Encode()
		a, err := c.do(http.MethodGet, u.String(), nil, false, false)
		if err != nil {
			return answer{}, err
		}
		if a.Status != http.StatusOK {
			// A refusal is the answer, and comparing it is the point of the step
			// that asked for it anonymously or without a token.
			return a, nil
		}
		var page struct {
			Items      []json.RawMessage `json:"items"`
			NextCursor *string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(a.Body, &page); err != nil {
			return a, nil
		}
		if envelope == nil {
			out = a
			if err := json.Unmarshal(a.Body, &envelope); err != nil {
				return a, nil
			}
		}
		items = append(items, page.Items...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
	}
	if envelope == nil {
		return out, nil
	}

	merged, err := json.Marshal(items)
	if err != nil {
		return answer{}, err
	}
	envelope["items"] = merged
	envelope["nextCursor"] = json.RawMessage("null")
	body, err := json.Marshal(envelope)
	if err != nil {
		return answer{}, err
	}
	out.Body = body
	return out, nil
}

// do issues one request.
//
// **`form` is not a convenience.** `redeemVoucher`, `acceptTerms` and
// `loginWithAccount` declare `application/x-www-form-urlencoded` in the contract
// and nothing else, because the portal submits them as document navigations from
// a `<form>` with JavaScript disabled. Sending JSON with a JSON content type to a
// generated handler that binds a form body produces a refusal about the media
// type — a difference the harness would have reported as a disagreement between
// the two servers rather than as its own request being wrong.
func (c *client) do(method, path string, body any, form, guest bool) (answer, error) {
	var rdr io.Reader
	contentType := ""
	switch {
	case body == nil:
	case form:
		values, err := formValues(body)
		if err != nil {
			return answer{}, err
		}
		rdr = strings.NewReader(values.Encode())
		contentType = "application/x-www-form-urlencoded"
	default:
		buf, err := json.Marshal(body)
		if err != nil {
			return answer{}, err
		}
		rdr = bytes.NewReader(buf)
		contentType = "application/json"
	}
	base := c.base
	if guest && c.portalBase != "" {
		base = c.portalBase
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		return answer{}, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.guest != nil {
		req.AddCookie(c.guest)
	}
	if c.csrf != "" {
		req.Header.Set("X-Foyer-CSRF", c.csrf)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return answer{}, err
	}

	// Both servers name their session cookie differently — the Go one carries
	// the `__Host-` prefix the contract requires and the mock over plain HTTP does
	// not — so the cookie is
	// matched by suffix. That difference is itself a finding, stated in the
	// report rather than hidden here.
	for _, ck := range resp.Cookies() {
		if strings.HasSuffix(ck.Name, "foyer_admin_session") && ck.Value != "" {
			c.cookie = ck
		}
		// The portal's cookie is spelled identically on both sides — `web/mock`
		// renamed it from `foyer_portal_client` when the contract's `portal` tag
		// named one — so it is matched exactly rather than by suffix. It is kept
		// in its own field because it must never be sent to the console.
		if ck.Name == portalCookieName && ck.Value != "" {
			c.guest = ck
		}
	}

	return answer{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Location:    resp.Header.Get("Location"),
		Body:        raw,
	}, nil
}
