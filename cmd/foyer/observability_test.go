package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/obs"
	"github.com/kiennt048/foyer/internal/portal"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testObservability is the observability a listener-routing test needs: a real
// registry, real handlers, and the logger writing to stderr so a failing test's
// output is in the test log where `go test` prints it.
//
// **It is a helper rather than each test building its own**, because the listeners
// take an `*observability` and a test that passed `nil` would be testing a
// different wiring from the one `run` builds — which is how a listener ends up
// unmetered in production and every routing test green. The checks are
// deliberately absent: a routing test is not a readiness test, and
// `TestTheReadinessEndpointIsMountedOnEveryListener` covers the endpoint.
func testObservability(t *testing.T) *observability {
	t.Helper()
	ob := &observability{
		log:       slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		metrics:   obs.NewMetrics(),
		readiness: obs.Readiness{},
	}
	// The portal access line is at Debug, so an Error-level logger keeps a guest's
	// page views out of a test's output while leaving a policy request visible.
	t.Cleanup(func() { ob.restore = func() {} })
	return ob
}

// testObservabilityWithPool is the same, with a readiness check that can be made
// to fail. See the two readiness tests in cmd/foyer for why both directions are
// proved against a real database.
func testObservabilityWithPool(t *testing.T, pool *pgxpool.Pool, loaded func() bool) *observability {
	t.Helper()
	ob := testObservability(t)
	ob.withChecks(pool, loaded)
	return ob
}

// TestTheMetricsEndpointIsOnThePolicyListenerAlone holds invariant 18 of
// `internal/obs`: `/metrics` answers on the Policy API and 404s on the other two.
//
// **It has to live here.** Mounting `/metrics` from
// `observability.healthHandlers` — the function every listener calls — is one line
// instead of three and the endpoint is harmless in a test. That turns this test
// red on the admin listener while every test in `internal/obs` stays green,
// because nothing in that package knows which listeners exist: the decision of *where* to mount is `cmd/foyer`'s, and the only
// thing that can check it is a test that builds all three.
//
// The exposure is the reason this is a security assertion rather than a tidiness
// one. `deploy/compose.yaml` publishes 80 and 8880 to every unauthenticated device
// on the SSID and 8443 to the venue's LAN; the metric carries this deployment's
// request counts, error rates and uptime, and its route labels name which
// operations exist. `deploy/verify.sh` holds the same property on an installed host,
// where a listener can be published that no Go test binds.
func TestTheMetricsEndpointIsOnThePolicyListenerAlone(t *testing.T) {
	ob := testObservability(t)
	srv := api.NewServer(nil)
	portalSvc := &portal.Service{}

	policy := policyListener(testConfig(), nil, ob)
	admin := adminListener(testConfig(), srv, ob)
	portal := portalListener(testConfig(), srv, portalSvc, ob)

	// The Policy API answers, and answers with an exposition.
	pts := httptest.NewServer(policy.srv.Handler)
	t.Cleanup(pts.Close)
	resp, err := pts.Client().Get(pts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics on the policy listener: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the policy listener answered %d on /metrics, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), metricNameForTest) {
		t.Errorf("the policy listener's /metrics does not carry the request counter:\n%s", body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("Content-Type = %q; a Prometheus 3 scrape fails on anything else", ct)
	}

	// The other two must 404. An unknown path on the admin listener is a 404 from
	// that listener rather than something the portal or the policy engine handles —
	// which is the invariant `TestTheAdminAndPolicyListenersRouteWhatTheyMount`
	// already holds for guest paths, and `/metrics` joins them.
	for _, tc := range []struct {
		name string
		srv  *namedServer
	}{
		{"admin console", admin},
		{"guest portal", portal},
	} {
		ts := httptest.NewServer(tc.srv.srv.Handler)
		resp, err := ts.Client().Get(ts.URL + "/metrics")
		if err != nil {
			ts.Close()
			t.Fatalf("GET /metrics on the %s: %v", tc.name, err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// **The assertion is "no exposition", not "404", and the difference was
		// measured rather than assumed.** The admin listener mounts the console's
		// single-page fallback on `/`, so `/metrics` there returns the console's
		// `index.html` with a 200 — which is not a disclosure, and a test demanding
		// 404 would have sent somebody to add a deny rule for a leak that was not
		// happening. The property worth holding is the one the decision is about: a
		// scrape pointed at this listener collects no metrics. The guest portal has
		// the same fallback, so the same reasoning covers it.
		if strings.Contains(string(got), metricNameForTest) || strings.Contains(resp.Header.Get("Content-Type"), "version=0.0.4") {
			t.Errorf("the %s served a metrics exposition on /metrics. It is reachable by %s, and a metrics endpoint there publishes this deployment's request counts, error rates and uptime to a party with no business knowing them. Body:\n%s",
				tc.name, reachOf(tc.name), got)
		}
		ts.Close()
	}
}

// reachOf names who can reach each listener, so a failure says why it matters
// rather than only that it happened.
func reachOf(listener string) string {
	switch listener {
	case "guest portal":
		return "every unauthenticated device on the SSID (compose.yaml publishes 80 and 8880)"
	default:
		return "anything on the venue's LAN (compose.yaml publishes 8443)"
	}
}

// metricNameForTest is the counter `internal/obs` publishes. It is written here
// rather than exported so a rename in that package does not silently keep a test
// asserting against a name that no longer exists — this test goes red instead.
const metricNameForTest = "foyer_http_requests_total"

// TestTheReadinessEndpointIsMountedOnEveryListener: `/readyz` answers on all
// three, and `/healthz` still answers on all three.
//
// Both are checked on every listener because a monitor may be pointed at any of
// them and `deploy/verify.sh` probes the console specifically. The two are
// separate requests in the same loop precisely so that a listener carrying one and
// not the other is a visible failure rather than a silent asymmetry.
func TestTheReadinessEndpointIsMountedOnEveryListener(t *testing.T) {
	ob := testObservability(t)
	// A failing check, so the endpoint is distinguishable from a 404: a readiness
	// answer of 503 proves the handler ran, and one of 200 would prove the check
	// wiring instead of the mount.
	ob.readiness.Network = func(context.Context) error { return errNetworkNotLoaded }
	// The network is NOT loaded here, which is the state a test runtime is in, so
	// the honest answer is 503 and the test asserts the mount rather than the
	// verdict.
	srv := api.NewServer(nil)
	for _, tc := range []struct {
		name string
		srv  *namedServer
	}{
		{"policy", policyListener(testConfig(), nil, ob)},
		{"admin", adminListener(testConfig(), srv, ob)},
		{"portal", portalListener(testConfig(), srv, &portal.Service{}, ob)},
	} {
		ts := httptest.NewServer(tc.srv.srv.Handler)
		t.Cleanup(ts.Close)

		live, err := ts.Client().Get(ts.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz on the %s: %v", tc.name, err)
		}
		liveBody, _ := io.ReadAll(live.Body)
		live.Body.Close()
		if live.StatusCode != http.StatusOK || string(liveBody) != "ok" {
			t.Errorf("the %s answered %d %q on /healthz, want 200 \"ok\"", tc.name, live.StatusCode, liveBody)
		}

		ready, err := ts.Client().Get(ts.URL + "/readyz")
		if err != nil {
			t.Fatalf("GET /readyz on the %s: %v", tc.name, err)
		}
		readyBody, _ := io.ReadAll(ready.Body)
		ready.Body.Close()
		// 503, not 404 and not 200: the handler ran, and it reports the network
		// check failing. A 200 here would mean the check was not installed.
		if ready.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("the %s answered %d on /readyz, want 503: the handler must run and report the network check failing. Body: %s", tc.name, ready.StatusCode, readyBody)
		}
		if !strings.Contains(string(readyBody), `"network"`) {
			t.Errorf("the %s's /readyz body does not name the failing check: %s", tc.name, readyBody)
		}
	}
}

// TestEveryListenerIsInstrumented holds the property `internal/obs` cannot check:
// a listener that exists but is not wrapped in the middleware records nothing and
// logs nothing, and every test in that package stays green.
//
// The portal is the case that matters. It is built from three different places —
// at boot, lazily when an operator uploads a certificate, and from a test — and
// `portalRuntime` carries `ob` on the struct so none of them can omit it. That is
// a structural argument; this is the behavioural one.
func TestEveryListenerIsInstrumented(t *testing.T) {
	ob := testObservability(t)
	// Count what the registry saw, by scraping it: the registry has no reader other
	// than its own exposition, which is the right shape for a metrics type.
	before := countSamples(ob, metricNameForTest)

	srv := api.NewServer(nil)
	listeners := []*namedServer{
		policyListener(testConfig(), nil, ob),
		adminListener(testConfig(), srv, ob),
		portalListener(testConfig(), srv, &portal.Service{}, ob),
	}
	for _, s := range listeners {
		ts := httptest.NewServer(s.srv.Handler)
		resp, err := ts.Client().Get(ts.URL + "/healthz")
		if err != nil {
			ts.Close()
			t.Fatalf("GET /healthz on the %s: %v", s.name, err)
		}
		resp.Body.Close()
		ts.Close()
	}

	after := countSamples(ob, metricNameForTest)
	if after-before != int64(len(listeners)) {
		t.Errorf("%d requests across %d listeners recorded %d metric samples, want one each.\n%s",
			len(listeners), len(listeners), after-before, ob.metrics.WriteText())
	}
}

// countSamples sums one counter's samples in the exposition.
func countSamples(ob *observability, name string) int64 {
	var n int64
	for _, line := range strings.Split(ob.metrics.WriteText(), "\n") {
		if strings.HasPrefix(line, name+"{") {
			n++
		}
	}
	return n
}

// TestTheProcessLoggerIsInstalledBeforeAnythingLogs is the ordering guarantee in
// `run`, held as a test on the thing it protects.
//
// `config.Load` can warn and the keyring can fail, both before the database is
// open, and without this ordering they would log through the unconfigured
// `slog.Default()` — a text handler on stderr carrying no request ID, in a process
// whose other lines are JSON. A support bundle would then hold two formats and no
// way to correlate them.
func TestTheProcessLoggerIsInstalledBeforeAnythingLogs(t *testing.T) {
	ob := newObservability(nil, nil)
	t.Cleanup(ob.restore)

	// `slog.Default()` is now this process's logger, with the context handler in
	// the chain, so a line written by a repository method deep inside a request
	// carries that request's ID.
	var buf bytes.Buffer
	restoreDefault := obs.Install(obs.NewLogger(&buf, slog.LevelDebug))
	t.Cleanup(restoreDefault)
	slog.Default().InfoContext(obs.WithRequestID(context.Background(), "0123456789abcdef0123456789abcdef"), "a line from below the handler")
	if !strings.Contains(buf.String(), "0123456789abcdef0123456789abcdef") {
		t.Errorf("a line logged through slog.Default() does not carry its request's ID:\n%s", buf.String())
	}
}

// TestThePolicyAPIStillRefusesRatherThanWarns holds the three `os.Exit(1)` calls
// in `policyTLS`.
//
// A logging change that swapped them for `slog.Error` would turn three refusals
// into three warnings and leave the Policy API serving authorization questions
// with no certificate and no client CA — which is the failure `internal/raddb`'s refusal
// and `deploy/compose.yaml`'s mTLS block exist to prevent. `slog` does not exit;
// that is what the explicit exits are for.
func TestThePolicyAPIStillRefusesRatherThanWarns(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func policyTLS(")
	if start < 0 {
		t.Fatal("func policyTLS is not in main.go")
	}
	policyTLS := body[start:]
	if end := strings.Index(policyTLS, "\nfunc "); end > 0 {
		policyTLS = policyTLS[:end]
	}
	// One exit per refusal: the certificate, the CA read, and the CA that parses
	// to nothing.
	if got := strings.Count(policyTLS, "os.Exit(1)"); got != 3 {
		t.Errorf("policyTLS has %d os.Exit(1) calls, want 3. Each is a refusal to serve authorization questions without the material §2 requires, and a converted log.Fatalf that only logs would leave the Policy API serving in that state.", got)
	}
	// The call, not the word: this function's own comment names `log.Fatalf` to
	// explain why the exits are explicit, and a substring search over the source
	// would match that sentence. Comments are stripped first so the check is about
	// code.
	if strings.Contains(stripComments(policyTLS), "log.Fatal") {
		t.Error("policyTLS still calls log.Fatalf; every line in this process is structured or the support bundle holds two formats")
	}
	// And the same for the console's own certificate.
	adminStart := strings.Index(body, "func adminListener(")
	if adminStart < 0 {
		t.Fatal("func adminListener is not in main.go")
	}
	admin := body[adminStart:]
	if end := strings.Index(admin, "\nfunc "); end > 0 {
		admin = admin[:end]
	}
	if !strings.Contains(admin, "os.Exit(1)") {
		t.Error("adminListener has no os.Exit(1): a console that cannot make a certificate must stop rather than serve a login form nobody can use")
	}
}

// stripComments removes Go comments, so a source scan asserts about code rather
// than about the prose explaining it. The function's own comment names
// `log.Fatalf` on purpose, and a scan that matched the sentence would be a test
// that cannot be satisfied by a correct fix.
func stripComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
