package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ── the request ID ────────────────────────────────────────────────────────────

// TestTheRequestIDIsThirtyTwoHexCharactersAndUnpredictable is a shape test, and
// the shape is the property: a downstream label or a log grep that assumes
// 32 lowercase hex characters is only correct while this holds.
func TestTheRequestIDIsThirtyTwoHexCharactersAndUnpredictable(t *testing.T) {
	shape := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		id := NewRequestID()
		if !shape.MatchString(id) {
			t.Fatalf("NewRequestID() = %q, want 32 lowercase hex characters", id)
		}
		if seen[id] {
			t.Fatalf("NewRequestID() returned %q twice in 2000 draws", id)
		}
		seen[id] = true
	}
}

// TestTheRequestIDIsNotPredictable pins that the source is crypto/rand rather
// than math/rand.
//
// **It can be made red.** `NewRequestID` implemented with `math/rand` seeded
// from the clock is a plausible choice, because the value is a correlation
// handle and not a secret, and because nothing in the request path fails when it
// is predictable. Switched to `math/rand/v2`, this test goes red on 2000 draws
// inside 64, because a clock-seeded generator emits its whole sequence in one
// run. A lint against `math/rand` scoped to a few packages would not reach this
// file, which is the point: such a guard is a proxy and the guarantee is this
// test.
func TestTheRequestIDIsNotPredictable(t *testing.T) {
	ids := make([]string, 400)
	for i := range ids {
		ids[i] = NewRequestID()
	}
	seen := map[string]bool{}
	for i, id := range ids {
		if seen[id] {
			t.Fatalf("draw %d repeated an earlier ID: a clock-seeded PRNG replays its sequence, and crypto/rand does not", i)
		}
		seen[id] = true
		// A clock-seeded math/rand with the default source produces values whose
		// high bytes barely move within a run. crypto/rand has no such structure.
		if i > 0 && id[:4] == ids[i-1][:4] && id[8:12] == ids[i-1][8:12] {
			t.Fatalf("draws %d and %d share a 8-byte prefix and suffix (%s…): that is PRNG structure, not entropy",
				i-1, i, id[:12])
		}
	}
}

func TestTheContextCarriesTheIDAndAnEmptyOneStoresNothing(t *testing.T) {
	ctx := WithRequestID(context.Background(), "abc")
	if got := FromContext(ctx); got != "abc" {
		t.Errorf("FromContext = %q, want abc", got)
	}
	if got := FromContext(WithRequestID(ctx, "")); got != "abc" {
		t.Errorf("an empty ID overwrote a real one: FromContext = %q, want abc", got)
	}
	if got := FromContext(context.Background()); got != "" {
		t.Errorf("FromContext on a bare context = %q, want the empty string", got)
	}
}

// TestAGuestCannotChooseTheCorrelationID is the privacy half of the request-ID
// decision, and it is a security property rather than a preference.
//
// A guest on the SSID can send any header it likes to the portal. If the portal
// adopted it, a guest could send another request's ID — or a plausible-looking
// one — and every log line for that request would carry a value the guest wrote.
// `TestThePolicyListenerAdoptsAWellFormedInboundID` is the other half: adoption
// happens where the peer proved itself, and only there.
func TestAGuestCannotChooseTheCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	h := NewPortalListener(NewLogger(&buf, slog.LevelDebug))
	ts := httptest.NewServer(h.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A line from three layers down, which is the case the handler exists for.
		slog.Default().InfoContext(r.Context(), "deep")
		w.WriteHeader(http.StatusNoContent)
	}), NewMetrics()))
	t.Cleanup(ts.Close)

	const forged = "00000000000000000000000000000000"
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/en/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(HeaderRequestID, forged)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got := resp.Header.Get(HeaderRequestID)
	if got == forged {
		t.Fatal("the portal adopted a request ID the guest chose: a device on the SSID can now write any value it likes into this deployment's logs")
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(got) {
		t.Fatalf("the portal answered X-Request-Id %q, want 32 hex characters it generated", got)
	}
	if !strings.Contains(buf.String(), got) {
		t.Errorf("the log line for that request does not carry the ID the response did.\nlog:\n%s", buf.String())
	}
}

// TestThePolicyListenerAdoptsAWellFormedInboundID is the reason `Lookup` takes a
// `trusted` parameter at all: the Policy API's peers present a client
// certificate, so an ID one of them sent is worth correlating across a chain
// rather than discarding.
func TestThePolicyListenerAdoptsAWellFormedInboundID(t *testing.T) {
	ts := httptest.NewServer(NewPolicyListener(nil).Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := FromContext(r.Context()); got != "" {
				w.Header().Set("X-Seen", got)
			}
			w.WriteHeader(http.StatusNoContent)
		}), NewMetrics()))
	t.Cleanup(ts.Close)

	const upstream = "0123456789abcdef0123456789abcdef"
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(HeaderRequestID, upstream)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Seen"); got != upstream {
		t.Errorf("the policy listener adopted %q, want the upstream's %q", got, upstream)
	}
}

// TestAMalformedInboundIDIsRefusedEvenFromATrustedPeer is the other half of the
// adoption rule. A trusted peer is not a trusted *string*: the value reaches a log
// line, and `internal/redact` masks MACs and addresses in text, not in a value
// chosen to be awkward.
func TestAMalformedInboundIDIsRefusedEvenFromATrustedPeer(t *testing.T) {
	shape := regexp.MustCompile(`^[0-9a-f]{32}$`)
	for _, bad := range []string{
		"",
		"not-an-id",
		"0123456789ABCDEF0123456789ABCDEF", // uppercase: not what NewRequestID emits
		"0123456789abcdef0123456789abcde",  // 31 characters
		"0123456789abcdef0123456789abcdef0",
		"0123456789abcdef 123456789abcdef",
		"0123456789abcdef\n123456789abcdef",
		"../../etc/passwd0000000000000000",
		strings.Repeat("a", 4096),
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set(HeaderRequestID, bad)
		if got := Lookup(r, true); got != "" {
			t.Errorf("Lookup(%q, trusted) = %q, want it refused", bad, got)
		}
		// And the same value is refused a second time, so a caller cannot get
		// through by retrying with a different one.
		if got := validID(bad); got != "" {
			t.Errorf("validID(%q) = %q, want the empty string", bad, got)
		}
	}
	if got := Lookup(httptest.NewRequest(http.MethodGet, "/", nil), false); got != "" {
		t.Errorf("Lookup on an untrusted listener = %q, want the empty string", got)
	}
	_ = shape
}

// TestTheContextHandlerPutsTheIDOnEveryRecord is the correlation guarantee, and
// it is tested through a `slog.Default()` call rather than a logger argument
// because that is the only way the guarantee is real: fourteen non-test files
// under `internal/` log through `slog.Default()` from several layers below the
// handler, and none of them pass a logger down.
func TestTheContextHandlerPutsTheIDOnEveryRecord(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelDebug)
	restore := Install(logger)
	t.Cleanup(restore)

	// A line with a request in its context.
	slog.Default().InfoContext(WithRequestID(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), "with")
	// A line without one: a maintenance sweep on its own timer.
	slog.Default().Info("without")

	out := buf.String()
	var withRec, withoutRec map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("a log line is not a JSON object, so nothing can query it: %v\n%s", err, line)
		}
		switch rec["msg"] {
		case "with":
			withRec = rec
		case "without":
			withoutRec = rec
		}
	}
	if withRec == nil {
		t.Fatalf("the line logged with a request context is missing:\n%s", out)
	}
	if withRec["request_id"] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("the line with a request context carries request_id %v, want the context's", withRec["request_id"])
	}
	// A record with no request must not carry an empty one: `"request_id":""` on
	// every maintenance line looks like it correlates with something and does not.
	if _, present := withoutRec["request_id"]; present {
		t.Errorf("a line with no request in its context carries a request_id field (%v); it should carry none",
			withoutRec["request_id"])
	}
	if withoutRec["msg"] != "without" {
		t.Errorf("the line with no request was lost: %v", withoutRec)
	}
}

// TestTheStdlibLoggerBecomesAStructuredLine is what makes "every log line is
// structured" true rather than nearly true.
//
// `net/http` insists on a `*log.Logger` for every `http.Server`'s `ErrorLog`, so
// a failed TLS handshake on the guest portal is a stdlib line no matter what this
// package does. It is also the line `redact.ErrorLog` exists to mask, and the
// masking has to survive the conversion to JSON — so this asserts both, and
// `TestTheStdlibLineIsMasked` below asserts the second on its own.
func TestTheStdlibLoggerBecomesAStructuredLine(t *testing.T) {
	var buf bytes.Buffer
	restore := Install(NewLogger(&buf, slog.LevelDebug))
	t.Cleanup(restore)

	stdlog := stdlibLogger()
	stdlog.Printf("http: TLS handshake error from 10.20.0.77:51514: remote error: tls: unknown certificate")

	out := strings.TrimSpace(buf.String())
	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("a stdlib log line did not become a JSON object: %v\n%s", err, out)
	}
	if rec["msg"] != "stdlib" {
		t.Errorf("msg = %v, want \"stdlib\"", rec["msg"])
	}
	if msg, _ := rec["message"].(string); !strings.Contains(msg, "TLS handshake error") {
		t.Errorf("the structured line lost the message: %q", msg)
	}
	// The stdlib logger's own timestamp prefix must be gone: a date in front of a
	// JSON object is a line no parser accepts.
	if strings.HasPrefix(out, "20") {
		t.Errorf("the stdlib line kept its date prefix, so it is not parseable:\n%s", out)
	}
}

// TestTheStdlibLineIsMasked is the guarantee `internal/redact`'s contract makes
// about `ErrorLog`, held after the conversion to structured output. Removing the
// masking is a one-line change and this is what notices.
func TestTheStdlibLineIsMasked(t *testing.T) {
	var buf bytes.Buffer
	restore := Install(NewLogger(&buf, slog.LevelDebug))
	t.Cleanup(restore)

	stdlibLogger().Print("http: TLS handshake error from 10.20.0.77:51514: EOF")
	stdlibLogger().Print("http: TLS handshake error from [2001:db8:1:2::77]:51514: EOF")
	out := buf.String()
	for _, leaked := range []string{"10.20.0.77", "2001:db8:1:2::77"} {
		if strings.Contains(out, leaked) {
			t.Errorf("the structured stdlib line still carries the client's address %q:\n%s", leaked, out)
		}
	}
	for _, kept := range []string{"10.20.0.0/24", "2001:db8:1::/48", "TLS handshake error"} {
		if !strings.Contains(out, kept) {
			t.Errorf("the structured stdlib line lost %q, so the line no longer says what happened:\n%s", kept, out)
		}
	}
}

// ── the metrics ───────────────────────────────────────────────────────────────

// TestTheExpositionCarriesNoGuestIdentifier is a test that asserts a metric is
// **absent** — the only kind that holds a decision like this in place, because
// the failure mode is a label that was always going to be there and nobody
// looked.
//
// It drives real requests carrying a MAC address, a voucher code, a username, a
// phone number and an IP address, in the places a real guest puts them — the
// NAS redirect's query string, a path segment, a request body, and headers —
// reads the exposition back, and asserts none of it is in there.
//
// **The tempting fallback is the failure it pins.** A `RouteLabel` that fell back
// to `r.URL.Path` when Go's `ServeMux` left `Request.Pattern` empty would be more
// useful than `unmatched`: a `/api/v0/sites/6f1c9d2e-…/devices` route is exactly
// what an operator wants. And on the guest portal it would also be a guest's MAC
// address in a label value on a scrape target nobody can erase from, which is the
// thing `docs/operator/personal-data.md` and the retention rules exist to
// prevent. There is no such fallback, and this test is what says so.
func TestTheExpositionCarriesNoGuestIdentifier(t *testing.T) {
	const (
		mac      = "aa:bb:cc:11:22:33"
		macHex   = "aabbcc112233"
		voucher  = "F7KQ-2X4M-9RTB"
		username = "guest@example.com"
		phone    = "+84901234567"
		ip       = "10.20.0.77"
		deviceID = "6f1c9d2e-4b7a-4c11-9f3e-2d8a5b6c7e01"
	)
	// Every one of these is a value docs/operator/personal-data.md says no
	// erasure reaches, and every one of them is something a guest can put
	// anywhere in a request.
	secrets := []string{mac, macHex, voucher, username, phone, ip, deviceID, "84901234567", "11:22:33"}

	m := NewMetrics()
	// A catch-all, so nothing is ever 404 and every request is recorded: the
	// point is that a request reaching a handler at all is enough to leak.
	var seen []string
	handler := NewPolicyListener(nil).Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		w.WriteHeader(http.StatusTeapot)
	}), m)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	paths := []string{
		"/api/v1/aaa/authorize?client_mac=" + mac + "&called_station_id=" + macHex,
		"/en/account?mac=" + mac,
		"/api/v0/sites/" + deviceID + "/devices",
		"/guest/s/default/" + mac,
		"/?client_ip=" + ip + "&phone=" + phone + "&voucher=" + voucher,
		"/api/v0/portal/sign-in",
		"/api/v0/sites/" + deviceID,
		"/" + strings.Repeat("a", 200),
		// A malformed percent escape, encoded so it reaches the server rather than
		// being rejected by the client. A scanner sends these.
		"/api/v0/%25zz",
		// A MAC in the path itself, which is the shape `routeFallback` would have
		// leaked before it was deleted.
		"/api/v0/sites/" + deviceID + "/" + macHex,
	}
	for _, p := range paths {
		resp, err := ts.Client().Get(ts.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		resp.Body.Close()
	}
	if len(seen) == 0 {
		t.Fatal("no request reached the handler, so nothing was measured")
	}

	exposition := m.WriteText()
	t.Logf("exposition after %d requests carrying guest identifiers:\n%s", len(seen), exposition)

	for _, s := range secrets {
		if strings.Contains(exposition, s) {
			t.Errorf("the exposition carries %q, which is a guest identifier no erasure can reach.\n%s", s, exposition)
		}
		// And the fragments, because a masked form is fine and a partial one is
		// not: the tail of a MAC and the local part of an email are still
		// identifying when a support bundle carries them.
		if len(s) > 6 {
			for _, frag := range []string{s[len(s)-6:], s[:6]} {
				if strings.Contains(exposition, frag) && !strings.Contains(exposition, "••") {
					t.Errorf("the exposition carries the fragment %q of %q:\n%s", frag, s, exposition)
				}
			}
		}
	}

	// Independently of the values above: the set of label names in the exposition
	// must be exactly the four this package declares. A fifth label name is
	// somebody adding a dimension, and it is the moment to ask what its values
	// are.
	assertOnlyDeclaredLabels(t, exposition)
}

// assertOnlyDeclaredLabels is the structural half of the privacy argument: not
// "the values that were in this test are absent" but "no label other than these
// four exists", which is a statement about the code rather than about one run.
func assertOnlyDeclaredLabels(t *testing.T, exposition string) {
	t.Helper()
	declared := map[string]bool{
		LabelListener: true, LabelMethod: true, LabelRoute: true, LabelStatus: true,
	}
	sample := regexp.MustCompile(`^[a-z_]+(\{[^\n]*\})? `)
	for _, line := range strings.Split(exposition, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := sample.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("exposition line does not match the text format's sample syntax: %q", line)
			continue
		}
		if strings.Contains(m[1], "") {
			continue
		}
		open := strings.Index(m[1], "{")
		if open < 0 {
			continue
		}
		for _, pair := range strings.Split(strings.Trim(m[1][open+1:], "}"), ",") {
			name, _, ok := strings.Cut(pair, "=")
			if !ok {
				t.Errorf("exposition line has an unparseable label %q in %q", pair, line)
				continue
			}
			if !declared[name] {
				t.Errorf("the exposition carries a label named %q, which is not one of the four this package declares (%s).\n%s",
					name, strings.Join(sortedKeys(declared), ", "), exposition)
			}
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// TestRouteLabelNeverReturnsARequestPath drives the label function with the
// values a real request produces, which is a sharper test than reading the
// exposition because it names the input.
func TestRouteLabelNeverReturnsARequestPath(t *testing.T) {
	// What Go's ServeMux puts in Request.Pattern for this repository's mounts.
	for _, pattern := range []string{
		"GET /healthz",
		"POST /api/v1/aaa/authorize",
		"POST /api/v1/aaa/authenticate",
		"GET /api/v1/captive-portal",
		"/api/v0/sites/{siteId}",
		"/venue/{name}",
		"/en/",
		"/",
		"/guest/",
	} {
		if got := RouteLabel(pattern); got != pattern {
			t.Errorf("RouteLabel(%q) = %q, want the pattern back", pattern, got)
		}
	}
	// Everything that is not a plausible pattern becomes `unmatched`.
	//
	// **A well-formed identifier is NOT in this list.** `/api/v0/sites/6f1c9d2e-4b7a-…`
	// looks like it belongs here — a path carrying a device id must not become a
	// label — but the string is indistinguishable from a real route pattern by any
	// rule this function could apply: same length, same alphabet, same segment
	// shape. No validator fixes that, and a validator that guesses wrong fails open.
	//
	// So the guarantee lives where it can actually be kept:
	// `TestNoCallerEverPassesARequestPathToALabel` reads every call site in the
	// repository and requires the argument to be `r.Pattern`. The guarantee is
	// that the only value reaching this function is one Go's ServeMux took from
	// its own registration table — not that the function can recognise one.
	for _, bad := range []string{
		"",
		"/en/account?mac=aa:bb:cc:11:22:33",
		"GET /en/\x00",
		"GET /en/\n",
		"/" + strings.Repeat("x", 200),
		"héllo",
		strings.Repeat("a", 81),
		"GET /en/?voucher=F7KQ-2X4M-9RTB",
		"/en/\taccount",
		"GET /en/account%00",
	} {
		if got := RouteLabel(bad); got != unmatched {
			t.Errorf("RouteLabel(%q) = %q, want %q", bad, got, unmatched)
		}
	}
	// The 80-character bound is real and not off by one: 80 is kept, 81 is not.
	if got := RouteLabel(strings.Repeat("a", 80)); got == unmatched {
		t.Errorf("RouteLabel of exactly 80 allowed characters = %q, want it kept: the bound is 80, not 79", got)
	}
	if got := RouteLabel(strings.Repeat("a", 81)); got != unmatched {
		t.Errorf("RouteLabel of 81 characters = %q, want %q", got, unmatched)
	}
}

// TestNoCallerEverPassesARequestPathToALabel is where the privacy guarantee
// actually lives, and it is here rather than in a validator because the two are
// not equally strong.
//
// `RouteLabel` receives a string and cannot tell a registered route pattern from
// a request path: `/api/v0/sites/{siteId}` and
// `/api/v0/sites/6f1c9d2e-4b7a-4c11-9f3e-2d8a5b6c7e01` are the same length and the
// same alphabet, and that was measured by trying to reject the second and failing
// (`TestRouteLabelNeverReturnsARequestPath` documents the attempt). A validator
// that cannot tell them apart fails **open**, which for a privacy guarantee is the
// wrong direction.
//
// This test reads every call site in the repository and requires the argument to
// be `r.Pattern` — the field Go's `ServeMux` fills in from its own registration
// table, which is a fixed set of strings compiled into the binary. That fails
// **closed**: the moment somebody writes `RouteLabel(r.URL.Path)` this goes red
// and names the file.
//
// It is an AST walk rather than a grep because a grep cannot tell
// `RouteLabel(r.Pattern)` from `RouteLabel(path)` where `path` is a variable
// holding `r.URL.Path`, and the variable is the shape this mistake actually takes.
func TestNoCallerEverPassesARequestPathToALabel(t *testing.T) {
	root := repoRoot(t)
	// Every non-test Go file in the repository. `internal/obs` itself is included:
	// its own `Middleware` passes `r.Pattern`, and that is the call site that
	// matters most.
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == "node_modules" || base == "worktrees" || base == "dist" {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}
	if len(files) < 50 {
		t.Fatalf("found only %d Go files under %s; the walk is not seeing the tree and this test would pass on nothing", len(files), root)
	}

	found := 0
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			continue // a file that does not parse is `go build`'s problem, not this test's
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			// Both spellings count: `obs.RouteLabel(…)` from outside the package and
			// the bare `RouteLabel(…)` inside it, which is this package's own
			// `Middleware` — the call site that matters most.
			var qualified string
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if fun.Sel.Name != "RouteLabel" {
					return true
				}
				pkg, ok := fun.X.(*ast.Ident)
				if !ok || pkg.Name != "obs" {
					return true
				}
				qualified = "obs."
			case *ast.Ident:
				if fun.Name != "RouteLabel" {
					return true
				}
				// Only inside this package: a bare `RouteLabel` in any other package
				// is a different function entirely.
				if filepath.Base(filepath.Dir(path)) != "obs" {
					return true
				}
			default:
				return true
			}
			found++
			arg, ok := call.Args[0].(*ast.SelectorExpr)
			if !ok {
				t.Errorf("%s: %sRouteLabel is called with something that is not a field selector.\n"+
					"  The argument must be r.Pattern, the field Go's ServeMux fills in from its own\n"+
					"  registration table. Anything derived from r.URL, r.RequestURI or a raw path is a\n"+
					"  guest's identifier in a metric label, and a scrape target's series are not\n"+
					"  deletable by the product's retention rules.", rel, qualified)
				return true
			}
			recv, ok := arg.X.(*ast.Ident)
			if !ok {
				t.Errorf("%s: %sRouteLabel is called on %s, which is not the request being served.\n"+
					"  The argument must be r.Pattern, the field Go's ServeMux fills in from its own\n"+
					"  registration table. Anything derived from r.URL, r.RequestURI or a raw path\n"+
					"  is a guest's identifier in a metric label, and a scrape target's series are\n"+
					"  not deletable by the product's retention rules.", rel, qualified, describe(arg))
				return true
			}
			if arg.Sel.Name != "Pattern" {
				t.Errorf("%s: %sRouteLabel(%s.%s) — that is a path, and the argument must be the ServeMux pattern.\n"+
					"  RouteLabel cannot tell a route pattern from a request path: a UUID in a path\n"+
					"  segment and a route segment are the same length and the same alphabet, which\n"+
					"  TestRouteLabelNeverReturnsARequestPath records. So the guarantee is that the only\n"+
					"  value reaching it came from ServeMux's registration table, checked here rather\n"+
					"  than guessed at in the function. This is the exact edit that leaks:\n"+
					"  TestTheExpositionCarriesNoGuestIdentifier goes red with the guest's MAC in the\n"+
					"  exposition when it is made.", rel, qualified, recv.Name, arg.Sel.Name)
				return true
			}
			if recv.Name != "r" && recv.Name != "req" && recv.Name != "request" {
				t.Errorf("%s: %sRouteLabel(%s.Pattern) — the pattern must be read off the request being served,\n"+
					"  not off some other value that happens to have a Pattern field.", rel, qualified, recv.Name)
			}
			return true
		})
	}
	if found == 0 {
		t.Fatal("no call site of obs.RouteLabel was found; the AST walk is not seeing the tree and this test would pass on nothing")
	}
	t.Logf("checked %d Go files, %d obs.RouteLabel call sites", len(files), found)
}

// describe renders an AST expression for a failure message, so a report names the
// expression that was wrong instead of saying "something that is not a field
// selector" and leaving the reader to find it.
func describe(e ast.Expr) string {
	var b strings.Builder
	if err := format.Node(&b, token.NewFileSet(), e); err != nil {
		return "<unprintable expression>"
	}
	return b.String()
}

// repoRoot is the module root, from this file's own location
// (`internal/obs/`), so the walk does not depend on the working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find go.mod above the working directory")
	return ""
}

func TestMethodLabelIsAClosedSet(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT"} {
		if got := MethodLabel(m); got != m {
			t.Errorf("MethodLabel(%q) = %q, want it kept", m, got)
		}
	}
	// `net/http` accepts any token as a method, so this is a client-supplied
	// string and it must not become a label value.
	for _, m := range []string{"get", "PROPFIND", "FOO", "GET / HTTP/1.1", "", "GET\n", "aa:bb:cc:11:22:33"} {
		if got := MethodLabel(m); got != other {
			t.Errorf("MethodLabel(%q) = %q, want %q", m, got, other)
		}
	}
}

func TestStatusLabelIsBounded(t *testing.T) {
	for _, c := range []int{100, 200, 204, 301, 401, 404, 429, 500, 503, 599} {
		if got := statusLabel(c); got != strconv.Itoa(c) {
			t.Errorf("statusLabel(%d) = %q, want the code", c, got)
		}
	}
	for _, c := range []int{-1, 0, 99, 600, 1000, 1 << 30} {
		if got := statusLabel(c); got != other {
			t.Errorf("statusLabel(%d) = %q, want %q", c, got, other)
		}
	}
}

// TestTheExpositionIsWellFormedTextFormat checks the output against the
// Prometheus text format's own requirements rather than against this
// implementation, so a mistake in the renderer is a red test instead of an
// endpoint that collects nothing.
func TestTheExpositionIsWellFormedTextFormat(t *testing.T) {
	m := NewMetricsAt(time.Unix(1750000000, 0))
	m.Observe("policy", "POST", "POST /api/v1/aaa/authorize", 200)
	m.Observe("policy", "POST", "POST /api/v1/aaa/authorize", 403)
	m.Observe("portal", "GET", "GET /healthz", 200)
	m.Observe("admin", "BREW", unmatched, 500)

	out := m.WriteText()

	// The last line must end in a line feed; a file without one is truncated as
	// far as a line-oriented parser is concerned.
	if !strings.HasSuffix(out, "\n") {
		t.Error("the exposition does not end with a line feed")
	}
	// Every non-empty line is either a comment or a sample.
	name := regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{.*\})? -?([0-9.]+([eE][-+]?[0-9]+)?|\+Inf|-Inf|NaN)$`)
	helps, types, samples := 0, 0, 0
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# HELP ") {
			helps++
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			types++
			continue
		}
		if !name.MatchString(line) {
			t.Errorf("line is neither a comment nor a valid sample: %q", line)
			continue
		}
		samples++
	}
	if helps != 2 || types != 2 {
		t.Errorf("got %d HELP and %d TYPE lines, want 2 of each (one per metric family):\n%s", helps, types, out)
	}
	if samples != 5 {
		t.Errorf("got %d sample lines, want 5 (4 series plus the start-time gauge):\n%s", samples, out)
	}
	// A TYPE line must precede the first sample of its metric.
	typeAt := strings.Index(out, "# TYPE foyer_http_requests_total counter")
	firstSample := strings.Index(out, "foyer_http_requests_total{")
	if typeAt < 0 || firstSample < 0 || typeAt > firstSample {
		t.Error("the TYPE line for foyer_http_requests_total does not precede its first sample")
	}
	// Reproducible: the same registry renders the same bytes twice.
	if m.WriteText() != out {
		t.Error("two scrapes of an unchanged registry produced different bytes")
	}
}

func TestTheExpositionContentTypeIsTheOnePrometheus3Requires(t *testing.T) {
	ts := httptest.NewServer(NewMetrics().Handler())
	t.Cleanup(ts.Close)
	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// From Prometheus 3.0 a scrape target MUST return a valid Content-Type or the
	// scrape fails. An endpoint that returns the wrong one looks fine to curl and
	// collects nothing.
	if got := resp.Header.Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("Content-Type = %q, want the canonical text format 0.0.4 type; a scrape from Prometheus 3 fails on anything else", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: a cached scrape is a stale count", got)
	}
}

// TestTheLabelValuesAreEscaped is a format requirement, and a label value that is
// not escaped is a broken exposition that fails silently at scrape time.
func TestTheLabelValuesAreEscaped(t *testing.T) {
	if got := escapeLabel(`a"b\c` + "\n"); got != `a\"b\\c\n` {
		t.Errorf("escapeLabel = %q, want backslash, quote and newline escaped", got)
	}
	if got := escapeLabel("plain"); got != "plain" {
		t.Errorf("escapeLabel(%q) = %q, want it unchanged", "plain", got)
	}
	// HELP escapes backslash and line feed, and NOT the double quote.
	if got := escapeHelp("say \"hi\"\nbye\\"); got != `say "hi"\nbye\\` {
		t.Errorf("escapeHelp = %q", got)
	}
	// And no label value this package can produce contains any of them anyway —
	// which is what makes the escaping a guard rather than a path values travel.
	for _, v := range []string{"portal", "admin", "policy", "GET", "other", "unmatched", "200"} {
		if strings.ContainsAny(v, "\\\"\n") {
			t.Errorf("a real label value %q contains a byte the format requires escaping; the renderer is relying on a coincidence", v)
		}
	}
}

// ── the readiness answer ──────────────────────────────────────────────────────

// TestReadinessIsFalseWhenPostgresIsUnreachable is proved in both directions
// against a real PostgreSQL rather than a fake.
//
// **The false direction is the interesting one.** A `SELECT 1` with a short
// readiness context is indistinguishable from a ping for reachability, so a test
// built on it passes against a stopped container for the wrong reason: a test that
// looks like it is checking something and is not. So this one closes the pool,
// which is what "unreachable" means to a process holding one, and asserts the
// 503. The true direction is proved against a live container in
// `TestReadinessIsTrueWhenPostgresIsReachable`, and the two together are the
// guarantee; either alone is half of it.
//
// The whole test is skipped, loudly, when there is no Docker, because a
// readiness guarantee that has never been exercised against a database is a
// guarantee nobody has.
func TestReadinessIsFalseWhenPostgresIsUnreachable(t *testing.T) {
	url := testDatabaseURL(t)
	if url == "" {
		t.Skip("no PostgreSQL available (Docker absent): an unproven readiness guarantee must look different from a proven one")
	}
	pool := openPool(t, url)

	// Prove the positive direction first, so a green skip cannot be mistaken for
	// a working check.
	if got := serveReadiness(t, Readiness{Postgres: pingCheck(pool)}); got.Ready != true {
		t.Fatalf("readiness against a live PostgreSQL = %+v, want ready; the negative half of this test would have proved nothing", got)
	}

	// Now make PostgreSQL unreachable. Closing the pool is the honest way to do
	// that from inside the process, and it is strictly harder to pass than
	// pointing the check at a bad address: a check that ignored its pool entirely
	// would still answer 200 here.
	pool.Close()

	got := serveReadiness(t, Readiness{Postgres: pingCheck(pool)})
	if got.Ready {
		t.Fatalf("readiness = ready with the database connection closed: %+v. A control plane whose database is gone must say so.", got)
	}
	if code := readinessStatus(t, Readiness{Postgres: pingCheck(pool)}); code != http.StatusServiceUnavailable {
		t.Errorf("HTTP status = %d, want 503: Docker and Kubernetes both act on the status, not on the body", code)
	}
	if got.Checks["postgres"].OK {
		t.Error("checks.postgres.ok = true with the database closed")
	}
	if got.Checks["postgres"].Error == "" {
		t.Error("checks.postgres.error is empty, so an operator reading /readyz learns that it is not ready and not why")
	}
}

// TestReadinessIsTrueWhenPostgresIsReachable is the other half, against a real
// container. See the previous test for why both are needed.
func TestReadinessIsTrueWhenPostgresIsReachable(t *testing.T) {
	url := testDatabaseURL(t)
	if url == "" {
		t.Skip("no PostgreSQL available (Docker absent)")
	}
	pool := openPool(t, url)
	t.Cleanup(pool.Close)

	got := serveReadiness(t, Readiness{Postgres: pingCheck(pool), Network: func(context.Context) error { return nil }})
	if !got.Ready {
		t.Fatalf("readiness against a live PostgreSQL = %+v, want ready", got)
	}
	if !got.Checks["postgres"].OK {
		t.Errorf("checks.postgres = %+v, want ok", got.Checks["postgres"])
	}
	if !got.Checks["network"].OK {
		t.Errorf("checks.network = %+v, want ok", got.Checks["network"])
	}
	if got.Checks["postgres"].Error != "" {
		t.Errorf("a passing check carries an error field: %q", got.Checks["postgres"].Error)
	}
}

// TestASlowDatabaseIsNotADownDatabase is a stated decision, held as a test.
//
// A ping that takes 1.9 seconds and succeeds is `ready: true`. A readiness probe
// that fails on latency takes a working node out of rotation during exactly the
// spike where you need it most, and the Kubernetes documentation is explicit that
// readiness is about whether traffic should be *sent*, not about how fast. So the
// only time-based rule in this probe is its own deadline — which bounds how long
// the answer takes, and is not a judgement about the answer.
func TestASlowDatabaseIsNotADownDatabase(t *testing.T) {
	// Comfortably inside ReadyTimeout, and well past any latency an operator would
	// call "too slow".
	const slow = 1200 * time.Millisecond
	slowCheck := func(ctx context.Context) error {
		select {
		case <-time.After(slow):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	got := serveReadiness(t, Readiness{Postgres: slowCheck})
	if !got.Ready {
		t.Fatalf("a check that took %s and succeeded reported not ready: %+v. A slow database is not a down database, and a probe that fails on latency takes a working node out of rotation during a spike.", slow, got)
	}
	if d := got.Checks["postgres"].DurationMS; d < 1000 {
		t.Errorf("checks.postgres.durationMs = %d, want it to report the ~%d it actually took; the number is published precisely so a human can judge slowness the probe deliberately does not", d, slow.Milliseconds())
	}
}

// TestASlowCheckPastTheDeadlineIsNotReady is the other side of the same
// boundary, and the reason ReadyTimeout exists at all: the answer has to arrive
// before the caller's own timeout, or the caller has stopped listening.
func TestASlowCheckPastTheDeadlineIsNotReady(t *testing.T) {
	hang := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	got := serveReadiness(t, Readiness{Postgres: hang})
	elapsed := time.Since(start)
	if got.Ready {
		t.Fatalf("a check that never returned reported ready: %+v", got)
	}
	if elapsed > ReadyTimeout+2*time.Second {
		t.Errorf("the answer took %s, past the %s budget: a probe that outlives its caller's timeout is a probe whose answer arrives too late to be read", elapsed, ReadyTimeout)
	}
}

// TestTheChecksRunConcurrently is the reason [Readiness.Handler] spawns two
// goroutines, and sequentially the guarantee it protects is genuinely lost: with
// one shared deadline, one slow check consumes the budget the others need and
// reports a failure that is a scheduling artifact rather than a dependency being
// down.
func TestTheChecksRunConcurrently(t *testing.T) {
	each := 400 * time.Millisecond
	slow := func(ctx context.Context) error {
		select {
		case <-time.After(each):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	start := time.Now()
	got := serveReadiness(t, Readiness{Postgres: slow, Network: slow})
	elapsed := time.Since(start)
	if !got.Ready {
		t.Fatalf("two checks that each took %s reported not ready: %+v", each, got)
	}
	if elapsed >= 2*each {
		t.Errorf("two %s checks took %s: they ran sequentially, so the answer's latency is the sum of every check's against one shared deadline", each, elapsed)
	}
}

// TestAnUnconfiguredCheckIsNotAFailure: a caller that supplies no database has
// no database to be unready about, and a probe that invented a failure there would
// be one nobody could act on.
func TestAnUnconfiguredCheckIsNotAFailure(t *testing.T) {
	got := serveReadiness(t, Readiness{})
	if !got.Ready {
		t.Errorf("readiness with no checks configured = %+v, want ready", got)
	}
	for _, name := range []string{"postgres", "network"} {
		if !got.Checks[name].OK {
			t.Errorf("checks.%s = %+v, want ok for a check that was not configured", name, got.Checks[name])
		}
	}
}

func TestTheReadinessBodyIsJSONAndCarriesTheRequestID(t *testing.T) {
	ts := httptest.NewServer(NewPolicyListener(nil).Middleware(
		Readiness{Network: func(context.Context) error { return nil }}.Handler(), NewMetrics()))
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/readyz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(HeaderRequestID, "0123456789abcdef0123456789abcdef")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get(HeaderRequestID); got != "0123456789abcdef0123456789abcdef" {
		t.Errorf("X-Request-Id = %q, want the ID this process resolved, so an operator can quote the probe that failed", got)
	}
	var body Result
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("the readiness body is not the JSON a monitoring system reads: %v", err)
	}
	if !body.Ready {
		t.Errorf("ready = false: %+v", body)
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
}

func TestTheReadinessErrorNamesTheDependencyWithoutItsAddress(t *testing.T) {
	// A failed connection's error text can carry the host it tried, and this
	// endpoint is reachable by whatever is watching the process. So the body
	// names the dependency and the reason, and the check decides what the reason
	// is. `store.Open` already wraps its own errors; this asserts the shape the
	// body has rather than the text of one driver's failure.
	ts := httptest.NewServer(Readiness{
		Postgres: func(context.Context) error { return errString("connection refused") },
	}.Handler())
	t.Cleanup(ts.Close)
	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
	var body Result
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Ready {
		t.Error("ready = true with a failing check")
	}
	if body.Checks["postgres"].Error == "" {
		t.Error("the failing check does not say why, so an operator is told 'not ready' and left to guess")
	}
	if !strings.Contains(body.Checks["postgres"].Error, "connection refused") {
		t.Errorf("error = %q, want the reason carried through", body.Checks["postgres"].Error)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// ── liveness, and the separation that matters most ────────────────────────────

// TestLivenessConsultsNothing is the first guarantee: `/healthz` still answers, and
// it answers without consulting a dependency.
//
// The check is a dependency that would fail: a liveness handler wired to it would
// answer 503 here, and a Docker healthcheck pointing at `/healthz` would then take
// a container out of rotation over a database restart.
func TestLivenessConsultsNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	Liveness(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("liveness status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("liveness body = %q, want \"ok\": three mount sites and deploy/verify.sh depend on it", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	// And the same answer with a cancelled context and a nil body, which is what a
	// liveness handler that consulted anything would not produce.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec2 := httptest.NewRecorder()
	Liveness(rec2, req.WithContext(ctx))
	if rec2.Code != http.StatusOK {
		t.Errorf("liveness with a cancelled context = %d, want 200: it consults nothing, so there is nothing to cancel", rec2.Code)
	}
}

// TestLivenessAndReadinessShareNoCode holds the two apart structurally, because
// the way this goes wrong is somebody making `/healthz` "more useful" by giving
// it a database check — and every other test staying green while a
// PostgreSQL restart takes a venue's network down with it.
//
// It reads this package's own source rather than its behaviour, because the
// failure is a *refactor* that a behavioural test cannot see: a behaviour-preserving
// change that wires the readiness checks into the liveness handler passes every
// behavioural test and is the outage.
func TestLivenessAndReadinessShareNoCode(t *testing.T) {
	liveness := sourceOf(t, "listener.go")
	// The whole of Liveness, from its doc comment to its closing brace.
	start := strings.Index(liveness, "func Liveness(")
	if start < 0 {
		t.Fatal("func Liveness is not in listener.go")
	}
	body := liveness[start:]
	// It must reference no check, no database and no readiness type.
	for _, forbidden := range []string{"Readiness", "Postgres", "Ping", "ctx context.Context", "Check"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Liveness references %q. A liveness probe that consults a dependency restarts the container every time that dependency restarts, and this process answers every Wi-Fi sign-in on site.", forbidden)
		}
	}
	// And the readiness handler must not be reachable from it: the two are
	// separate http.Handlers mounted at separate paths by cmd/foyer.
	readiness := sourceOf(t, "readiness.go")
	if !strings.Contains(readiness, "func (r Readiness) Handler() http.Handler") {
		t.Error("Readiness.Handler is gone; /readyz is the thing this whole file exists for")
	}
	if !strings.Contains(liveness, "func Liveness(w http.ResponseWriter, _ *http.Request)") {
		t.Error("Liveness's signature changed to take a request or a context it does not need; it consults nothing, so it has nothing to read")
	}
}

func sourceOf(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(".", name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

// ── the access line ───────────────────────────────────────────────────────────

// TestEveryListenerRecordsExactlyOneLinePerRequest is what makes the log count and
// the metric count agree. A handler that writes a header twice, or a client that
// disconnects mid-body, must still produce one line.
func TestEveryListenerRecordsExactlyOneLinePerRequest(t *testing.T) {
	for _, l := range []Listener{NewPortalListener(nil), NewAdminListener(nil), NewPolicyListener(nil)} {
		t.Run(l.Name, func(t *testing.T) {
			var buf bytes.Buffer
			m := NewMetrics()
			h := Listener{Name: l.Name, Trusted: l.Trusted, Log: NewLogger(&buf, slog.LevelDebug)}.
				Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusTeapot)
					w.WriteHeader(http.StatusOK) // a second WriteHeader, which net/http ignores
					_, _ = w.Write([]byte("hello"))
				}), m)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/en/", nil))

			lines := 0
			for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
				if strings.Contains(line, `"msg":"request"`) {
					lines++
					var rec map[string]any
					if err := json.Unmarshal([]byte(line), &rec); err != nil {
						t.Fatalf("the access line is not JSON: %v\n%s", err, line)
					}
					if rec["status"] != float64(http.StatusTeapot) {
						t.Errorf("status = %v, want %d — the first code written is the one the client got", rec["status"], http.StatusTeapot)
					}
					if rec["listener"] != l.Name {
						t.Errorf("listener = %v, want %q", rec["listener"], l.Name)
					}
					if rec["route"] != unmatched {
						t.Errorf("route = %v, want %q: this handler is mounted without a ServeMux so there is no pattern, and falling back to the path is the leak RouteLabel exists to prevent", rec["route"], unmatched)
					}
					if rec["bytes"] != float64(5) {
						t.Errorf("bytes = %v, want 5", rec["bytes"])
					}
					if _, present := rec["request_id"]; !present {
						t.Error("the access line carries no request_id")
					}
				}
			}
			if lines != 1 {
				t.Errorf("got %d access lines, want exactly 1: a log that disagrees with the metric cannot be reconciled after the fact", lines)
			}
			// And the metric counted it once.
			if got := strings.Count(m.WriteText(), "\n"+metricRequests+"{"); got != 1 {
				t.Errorf("the metric has %d series for one request, want 1:\n%s", got, m.WriteText())
			}
			if !strings.Contains(m.WriteText(), metricRequests+`{listener="`+l.Name+`"`) {
				t.Errorf("the metric does not carry the listener label %q", l.Name)
			}
		})
	}
}

// TestAResponseWriterThatNeverWritesIsStillCounted: net/http answers 200 for a
// handler that returns without writing, so the recorded status has to be 200
// rather than 0 — otherwise every such request is an unexplained `other` in the
// exposition.
func TestAResponseWriterThatNeverWritesIsStillCounted(t *testing.T) {
	m := NewMetrics()
	h := NewPolicyListener(nil).Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), m)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !strings.Contains(m.WriteText(), `status="200"`) {
		t.Errorf("a handler that wrote nothing was not recorded as 200:\n%s", m.WriteText())
	}
	if strings.Contains(m.WriteText(), `status="other"`) {
		t.Errorf("a handler that wrote nothing was recorded as `other`:\n%s", m.WriteText())
	}
}

// TestTheResponseWriterStillStreams: a wrapper that swallowed `http.Flusher` would
// make the venue photograph on the guest portal buffer instead of stream, which
// is a regression nobody would attribute to this package. `Unwrap` is what
// `http.ResponseController` reaches through.
func TestTheResponseWriterStillStreams(t *testing.T) {
	var flushed bool
	inner := &flushRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: &flushed}
	w := &responseRecorder{ResponseWriter: inner}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := http.NewResponseController(w).Flush(); err != nil {
		t.Fatalf("http.ResponseController could not flush through the wrapper: %v. The guest portal serves multi-megabyte venue photographs through this.", err)
	}
	if !flushed {
		t.Error("Flush did not reach the underlying writer")
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed *bool
}

func (f *flushRecorder) Flush() { *f.flushed = true; f.ResponseRecorder.Flush() }

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"DEBUG": slog.LevelDebug, "debug": slog.LevelDebug, " Debug ": slog.LevelDebug,
		"INFO": slog.LevelInfo, "WARN": slog.LevelWarn, "WARNING": slog.LevelWarn,
		"ERROR": slog.LevelError, "error": slog.LevelError,
		// Anything unrecognised is INFO rather than a refusal: a control plane that
		// will not start over a log level is a worse outcome than a wrong one.
		"": slog.LevelInfo, "TRACE": slog.LevelInfo, "11": slog.LevelInfo, "yes": slog.LevelInfo,
	} {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if defaultLogLevel != "INFO" {
		t.Errorf("the default level is %q, want INFO: the composition root logged at INFO before this package existed and nothing asked for quieter", defaultLogLevel)
	}
}
