package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/kiennt048/foyer/internal/webui"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/portal"
)

// What the three listeners actually route.
//
// ── why this file exists ─────────────────────────────────────────────────────
//
// `mux.Handle("GET /", …)` beside `mux.Handle("/api/v0/", …)` is a pattern
// conflict `http.ServeMux` **panics on at registration**: `GET /` matches fewer
// methods and a more general path, so neither pattern is more specific and the mux
// refuses both. The process does not start.
//
// `go build ./...` and `go vet ./...` are silent on it. A
// panic in `main` is the one class of defect a compiler cannot see and a green
// suite will not catch unless something calls the function — so something does,
// here, and it is cheap: building a mux takes no database and binds no port.
//
// The stubs below are stubs, not fakes with behaviour. This asserts routing, and
// what each route decides is asserted where it can be: `internal/portal`,
// `internal/api` and `internal/apistore`.

// ── stub ports, with no behaviour and no permissive default ──────────────────

type stubCredentials struct{}

func (stubCredentials) ClaimVoucher(context.Context, string, string, string, time.Time) (portal.Claim, error) {
	return portal.Claim{}, portal.ErrVoucherUnknown
}

func (stubCredentials) AuthenticateGuest(context.Context, string, string, string) (portal.Guest, error) {
	return portal.Guest{}, portal.ErrBadCredentials
}

type stubSites struct{}

// Placement refuses every access point, which is what an unconfigured deployment
// looks like: no session is minted and nothing can be granted.
func (stubSites) Placement(context.Context, string) (portal.Placement, error) {
	return portal.Placement{}, portal.ErrNotFound
}

type stubDrivers struct{}

// Verify returns `PresenceUnavailable` **and** a non-nil error, which is the pair
// `portal.Classify` reads. A stub that returned only one of them would have this
// file quietly asserting a refusal for the wrong reason.
func (stubDrivers) Verify(context.Context, portal.Session) (nas.Presence, error) {
	return nas.Presence{Outcome: nas.PresenceUnavailable}, errors.New("no controller in a routing test")
}

func (stubDrivers) Authorize(context.Context, portal.Placement, portal.Grant) error {
	return errors.New("no controller in a routing test")
}

func (stubDrivers) HardQuota(context.Context, portal.Session) bool { return false }

func testPortalPorts() (portal.Ports, *portal.MemoryStore) {
	mem := portal.NewMemoryStore()
	return portal.Ports{
		Sessions:    mem,
		Grants:      mem,
		Credentials: stubCredentials{},
		Sites:       stubSites{},
		Drivers:     stubDrivers{},
	}, mem
}

func testConfig() config.Config {
	return config.Config{
		PolicyAddr: "127.0.0.1:0",
		AdminAddr:  "127.0.0.1:0",
		PortalAddr: "127.0.0.1:0",
		PortalURL:  "http://portal.example/en/",
		VenueURL:   "http://portal.example/venue",
	}
}

// TestThePortalListenerRoutesWhatItMounts.
//
// The first assertion is the one that would have caught a process that could not
// start: `portalListener` registers its patterns, and registering them is what
// panics when two conflict.
func TestThePortalListenerRoutesWhatItMounts(t *testing.T) {
	ports, grants := testPortalPorts()
	srv, err := api.New(api.Config{Portal: ports})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	svc, err := portal.NewService(portal.ServiceConfig{Ports: ports})
	if err != nil {
		t.Fatalf("portal.NewService: %v", err)
	}

	listener := portalListener(testConfig(), srv, svc, testObservability(t))
	ts := httptest.NewServer(listener.srv.Handler)
	t.Cleanup(ts.Close)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	get := func(path string) *http.Response {
		t.Helper()
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp
	}

	// The front door negotiates a language and redirects to a document. It is a
	// `302` rather than a `303` because it is not answering a form submission.
	root := get("/")
	defer root.Body.Close()
	if root.StatusCode != http.StatusFound {
		t.Errorf("GET / answered %d, want 302", root.StatusCode)
	}
	if loc := root.Header.Get("Location"); loc != "/en/" {
		t.Errorf("GET / redirected to %q, want /en/", loc)
	}

	// The UniFi redirect never lands on `/`: the controller appends
	// `/guest/s/<site>/` to the bare IP its External Portal field takes. That
	// path mounts the same front door — it reads only the query string, so the
	// path is transport — and drops nothing: the `id`/`ap` below must survive
	// into the document redirect, because they are the browser-to-MAC binding.
	guest := get("/guest/s/default/?ap=aa:bb:cc:dd:ee:ff&id=11:22:33:44:55:66&t=123&ssid=Guest-WiFi")
	defer guest.Body.Close()
	if guest.StatusCode != http.StatusFound {
		t.Errorf("GET the UniFi guest path answered %d, want 302", guest.StatusCode)
	}
	if loc := guest.Header.Get("Location"); !strings.HasPrefix(loc, "/en/?") ||
		!strings.Contains(loc, "id=11:22:33:44:55:66") ||
		!strings.Contains(loc, "ap=aa:bb:cc:dd:ee:ff") {
		t.Errorf("GET the UniFi guest path redirected to %q, want /en/ carrying the NAS query verbatim", loc)
	}

	// The contract's portal operations answer on this listener. `404` because no
	// session resolves, which is the honest answer for a browser that carries no
	// cookie — not `501`, which would mean the portal was never wired.
	status := get(api.BasePath + "/portal/status")
	defer status.Body.Close()
	if status.StatusCode != http.StatusNotFound {
		t.Errorf("GET %s/portal/status answered %d, want 404", api.BasePath, status.StatusCode)
	}

	// A console operation asked for on the guest listener is `404` and not `403`:
	// on this listener there is no such route, and `403` would say the route exists
	// and you may not have it — on the one listener bound wide.
	admin := get(api.BasePath + "/sites")
	defer admin.Body.Close()
	if admin.StatusCode != http.StatusNotFound {
		t.Errorf("GET %s/sites answered %d on the guest listener, want 404", api.BasePath, admin.StatusCode)
	}

	// RFC 8908's media type belongs to the captive-portal API, and this is the
	// route that carries it. The contract types `/portal/status` as
	// `application/json`; they are two endpoints, not one.
	capport := get("/api/v1/captive-portal")
	defer capport.Body.Close()
	if ct := capport.Header.Get("Content-Type"); ct != portal.MediaTypeCaptiveJSON {
		t.Errorf("the RFC 8908 endpoint answered %q, want %q", ct, portal.MediaTypeCaptiveJSON)
	}

	health := get("/healthz")
	defer health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz answered %d, want 200", health.StatusCode)
	}

	// Venue photographs live on this listener, as raw bytes beside the `/api`
	// tree: a guest's `<img>` loads them before authentication, and the
	// console's listener is unreachable from a guest. No photograph is stored
	// behind the stub ports, so this is 404 — what matters is that the portal
	// answers it rather than the document catch-all.
	venue := get("/venue/site-x/hero.jpg")
	defer venue.Body.Close()
	if venue.StatusCode != http.StatusNotFound {
		t.Errorf("GET /venue/... answered %d, want 404 for a site with no photograph", venue.StatusCode)
	}
	if ct := venue.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("GET /venue/... answered %q, want the asset handler's plain 404 rather than a document", ct)
	}
	// **The route that took a MAC from a request body is gone.** It authorized on
	// the strength of a value the caller typed, which is the failure
	// `docs/rules/security.md` §4 exists to forbid. Asserted on the outcome
	// rather than on a status code: the catch-all front door answers this path
	// too, so what matters is that nothing was granted.
	resp, err := client.PostForm(ts.URL+"/api/v1/portal/grant", url.Values{
		"method": {"click_through"}, "clientMac": {"aa:bb:cc:dd:ee:ff"},
	})
	if err != nil {
		t.Fatalf("POST the retired grant route: %v", err)
	}
	defer resp.Body.Close()
	if got := len(grants.Grants()); got != 0 {
		t.Errorf("the retired grant route produced %d grants for a MAC in a request body", got)
	}
}

// TestTheAdminAndPolicyListenersRouteWhatTheyMount.
//
// Same reason as above — a mux that panics is a process that does not start — and
// one property beyond it: the admin listener has no catch-all, so an unknown path
// is a `404` from this listener rather than something the portal or the policy
// engine handles. That absence is what keeps the three surfaces from quietly
// merging the next time somebody adds a route.
func TestTheAdminAndPolicyListenersRouteWhatTheyMount(t *testing.T) {
	admin := adminListener(testConfig(), api.NewServer(nil), testObservability(t))
	ts := httptest.NewServer(admin.srv.Handler)
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the admin listener answered %d on /healthz, want 200", resp.StatusCode)
	}

	stray, err := ts.Client().Get(ts.URL + "/en/")
	if err != nil {
		t.Fatalf("GET /en/: %v", err)
	}
	defer stray.Body.Close()
	if stray.StatusCode != http.StatusNotFound {
		t.Errorf("the admin listener answered %d for a guest document, want 404 — it has no catch-all",
			stray.StatusCode)
	}

	policy := policyListener(testConfig(), nil, testObservability(t))
	pts := httptest.NewServer(policy.srv.Handler)
	t.Cleanup(pts.Close)

	ph, err := pts.Client().Get(pts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz on the policy listener: %v", err)
	}
	defer ph.Body.Close()
	if ph.StatusCode != http.StatusOK {
		t.Errorf("the policy listener answered %d on /healthz, want 200", ph.StatusCode)
	}

	// The console's own base path is not on the policy listener:
	// `policyMux.Handle("/", apiServer.Handler())` would put the admin API and the
	// RADIUS authorization endpoint on one port.
	console, err := pts.Client().Get(pts.URL + api.BasePath + "/sites")
	if err != nil {
		t.Fatalf("GET the console path on the policy listener: %v", err)
	}
	defer console.Body.Close()
	if console.StatusCode != http.StatusNotFound {
		t.Errorf("the policy listener answered %d for %s/sites, want 404",
			console.StatusCode, api.BasePath)
	}
	if strings.Contains(console.Header.Get("Content-Type"), "problem") {
		t.Error("the policy listener answered a console problem document, so the API is mounted on it")
	}
}

// TestThePortalListenerServesADocumentAndNotALoop guards against a guest portal
// that cannot be used.
//
// The failure it pins: `GET /` answering `302` to `/en/`, and `/en/` answering
// `302` to `/en/` — an infinite redirect, twenty-six bytes, no HTML. That happens
// when nothing serves the documents and `mux.Handle("/", EntryHandler)` is the
// catch-all, so every path the frontend owns falls into the redirect that points
// back at it.
//
// **Coverage that asserts the redirect and stops there misses it.**
// `TestThePortalListenerRoutesWhatItMounts` above is green against that broken
// product, because it checks the routes the listener *mounts* — and the failure is
// a route it does not mount. That is the shape worth naming: a test
// can be complete about what was built and silent about what was not, and no amount
// of care inside it closes the gap. The question it never asked is *and then what*.
//
// So this asserts on the body. A `200` is not enough — a placeholder answers `200`
// — and neither is "not a redirect", because a `404` is also not a redirect.
func TestThePortalListenerServesADocumentAndNotALoop(t *testing.T) {
	if !webui.Built() {
		t.Skip("this binary carries no frontend: run `make web-embed`. " +
			"The condition is evaluable and expires on its own — it is not a phase.")
	}
	ports, _ := testPortalPorts()
	srv, err := api.New(api.Config{Portal: ports})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	svc, err := portal.NewService(portal.ServiceConfig{Ports: ports})
	if err != nil {
		t.Fatalf("portal.NewService: %v", err)
	}
	ts := httptest.NewServer(portalListener(testConfig(), srv, svc, testObservability(t)).srv.Handler)
	t.Cleanup(ts.Close)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	// Every language the embedded build actually carries, so a build that drops one
	// fails here rather than in front of a guest. Derived, never typed.
	for _, lang := range webui.PortalLanguages() {
		path := "/" + lang + "/"
		res, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()

		if res.StatusCode == http.StatusFound {
			t.Errorf("GET %s answered 302 to %q — this is the loop, exactly as measured "+
				"on the deployment: nothing serves the document, so the catch-all "+
				"redirects to the path that has no document",
				path, res.Header.Get("Location"))
			continue
		}
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s answered %d, want a document", path, res.StatusCode)
			continue
		}
		if !bytes.Contains(body, []byte("<html")) || !bytes.Contains(body, []byte("</html>")) {
			t.Errorf("GET %s answered %d and %d bytes that are not an HTML document; "+
				"a status check alone passes for a placeholder", path, res.StatusCode, len(body))
		}
	}

	// And the half that must NOT change: `/` is still the front door, still a
	// redirect, and still relays the query the NAS sent. Serving documents must not
	// have swallowed the one route that binds a browser to a MAC.
	res, err := client.Get(ts.URL + "/?ap=94:2a:6f:d0:30:57&id=11:22:33:44:55:66")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("GET / answered %d, want 302: the front door is what learns the MAC", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); !strings.Contains(loc, "ap=94:2a:6f:d0:30:57") {
		t.Errorf("GET / redirected to %q, losing the query: the NAS parameters are the "+
			"only evidence VerifyPresence has", loc)
	}
}

// TestThePortalCannotLoopEvenWithNoFrontendEmbedded is the assertion a naive
// repair of that mount fails.
//
// The failure is not "a document is missing". It is the handler which
// *redirects to* `/{lang}/` also being the handler that *receives* it — and any
// composition where those are the same handler can cycle. A fix that falls through
// to `EntryHandler` when the frontend has no document for a path puts the cycle
// straight back for a binary built without one: `/en/` → 302 `/en/` → 302, in a
// state `make web-embed` has not been run in and therefore every fresh clone.
//
// So this drives the unbuilt case deliberately, by asking for a language the
// embedded build cannot have. Anything is acceptable except a redirect to the path
// that was requested: a 404 is an answer, a 503 naming the missing build step is an
// answer, a document is an answer. A redirect to yourself is not.
func TestThePortalCannotLoopEvenWithNoFrontendEmbedded(t *testing.T) {
	ports, _ := testPortalPorts()
	srv, err := api.New(api.Config{Portal: ports})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	svc, err := portal.NewService(portal.ServiceConfig{Ports: ports})
	if err != nil {
		t.Fatalf("portal.NewService: %v", err)
	}
	ts := httptest.NewServer(portalListener(testConfig(), srv, svc, testObservability(t)).srv.Handler)
	t.Cleanup(ts.Close)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	// `zz` is a language no build carries, which is the unbuilt binary's situation
	// for every language at once. Derived as "not in the embedded set" rather than
	// assumed: if a build ever ships `zz`, this picks another.
	absent := "zz"
	for _, have := range webui.PortalLanguages() {
		if have == absent {
			absent = "qq"
		}
	}
	path := "/" + absent + "/"
	res, err := client.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	_ = res.Body.Close()

	// **The assertion is the invariant, not the symptom.** Checking for a redirect
	// whose Location equals the request path is the symptom — and this composition
	// never produces one: with a
	// built binary, `/zz/` falls to `EntryHandler`, which negotiates among the
	// languages that *do* exist and redirects to `/en/`. Different path, assertion
	// silent, sabotage green. The cycle needs an unbuilt binary, which a test binary
	// with an embedded frontend cannot become.
	//
	// What is checkable from here is the structural rule that makes the cycle
	// impossible in every state including the one this test cannot enter: **only
	// `/` redirects.** A language-prefixed path reaching a redirect means the front
	// door is answering paths the frontend owns, which is the shape, and it is
	// exactly what the fall-through repair does.
	if res.StatusCode == http.StatusFound || res.StatusCode == http.StatusMovedPermanently {
		t.Fatalf("GET %s answered %d to %q. Only `/` may redirect from this listener: a "+
			"language-prefixed path reaching the front door means the handler that "+
			"redirects to `/{lang}/` is also the handler that receives it, which is the "+
			"redirect cycle — reachable here for any language the "+
			"embedded build lacks, and for every language in a binary built without one",
			path, res.StatusCode, res.Header.Get("Location"))
	}
}

// TestTheConsoleNeverClaimsAGuestPath drives **both** states of the console mount,
// because a tree can only be in one of them, and a test that runs only in the
// usual one says nothing about the other.
//
// The invariant is the admin listener's most basic one: it never answers a guest document
// path. A naive mount holds that only for a binary with the frontend embedded —
// the decline list is `webui.PortalLanguages()`, which is empty when nothing is
// built, so the loop runs zero times and `/en/` reaches the console.
// `TestTheAdminAndPolicyListenersRouteWhatTheyMount` would then be **red in every
// fresh clone and green in every tree that had run `make web-embed`** — two
// harnesses, each able to express half a state, with the halves split by a build
// step rather than by a package boundary.
//
// So the console here is a stand-in rather than `webui.Admin()`: the real one is
// whichever half this tree happens to be in, and this test has to be in both. It
// answers `200` for anything it is given, which is what makes the assertion sharp —
// a guest path that reaches the console is a `200`, not a `503` that only looks
// wrong once you know the tree.
func TestTheConsoleNeverClaimsAGuestPath(t *testing.T) {
	console := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("the console"))
	})

	for _, tc := range []struct {
		name      string
		built     bool
		languages []string
		// console is the only path the console may answer in this state.
		consoleOwns string
	}{
		// The shipped image: documents embedded, languages read from them.
		{name: "frontend embedded", built: true, languages: []string{"en", "vi"}, consoleOwns: "/sessions"},
		// Every fresh clone, and the deploy image's build stage before `web-embed`.
		// Nothing to fall back to, so there is no catch-all and the root carries the
		// diagnostic that names the missing build step.
		{name: "no frontend embedded", built: false, languages: nil, consoleOwns: "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mountConsole(mux, tc.built, tc.languages, console)
			ts := httptest.NewServer(mux)
			t.Cleanup(ts.Close)

			// The guest's namespace, asked for the way a guest document path looks.
			// `en` is asserted in both states on purpose: it is the language the
			// portal is guaranteed to have, and the state that dropped it is the
			// state whose language list is empty.
			for _, path := range []string{"/en", "/en/", "/en/terms/", "/vi/"} {
				res, err := ts.Client().Get(ts.URL + path)
				if err != nil {
					t.Fatalf("GET %s: %v", path, err)
				}
				body, _ := io.ReadAll(res.Body)
				_ = res.Body.Close()
				if res.StatusCode != http.StatusNotFound {
					t.Errorf("GET %s answered %d (%q), want 404 — the admin listener claimed a guest path",
						path, res.StatusCode, body)
				}
			}

			// And the console still gets its own, or the repair is a deletion.
			res, err := ts.Client().Get(ts.URL + tc.consoleOwns)
			if err != nil {
				t.Fatalf("GET %s: %v", tc.consoleOwns, err)
			}
			_ = res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Errorf("GET %s answered %d, want the console — %s",
					tc.consoleOwns, res.StatusCode,
					"with no frontend that is the 503 naming the missing build step, which must stay reachable")
			}
		})
	}
}
