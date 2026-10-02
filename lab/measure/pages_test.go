package main

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *server {
	t.Helper()
	tlsm, err := ensureTLS(t.TempDir(), []net.IP{net.ParseIP("192.0.2.23")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ttf := buildProbeTTF()
	return &server{
		log:       newEventLog(50, nil, false),
		tlsm:      tlsm,
		captive:   true,
		httpHost:  "192.0.2.23:8080",
		httpsHost: "192.0.2.23:8443",
		ttf:       ttf,
		woff:      wrapWOFF(ttf),
	}
}

func get(t *testing.T, s *server, path string, overTLS bool) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("User-Agent", "CaptiveNetworkSupport-444.40.2 wispr")
	if overTLS {
		req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, CipherSuite: tls.TLS_AES_128_GCM_SHA256}
	}
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	return rec.Result()
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ─── the absolute-URL rule ───────────────────────────────────────────────────
//
// The portal's build asserts its output contains no external origin, because a
// portal that fetches one cannot render before the guest has internet. This kit
// serves pages into the same environment and carries the same rule, and the
// check is written the same way: references first, then the stricter whole-text
// scan for the probe page in particular.

var (
	refPattern      = regexp.MustCompile(`\b(?:src|href|srcset)\s*=\s*"([^"]+)"`)
	absolutePattern = regexp.MustCompile(`https?://|(?:^|[\s"'(])//[a-zA-Z0-9]`)
)

// externalRefs returns every src/href/srcset that names another origin.
func externalRefs(html string) []string {
	var out []string
	for _, m := range refPattern.FindAllStringSubmatch(html, -1) {
		ref := m[1]
		if strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#") {
			continue
		}
		if strings.HasPrefix(ref, "//") || regexp.MustCompile(`^https?://`).MatchString(ref) {
			out = append(out, ref)
		}
	}
	return out
}

func TestNoServedPageReferencesAnotherOrigin(t *testing.T) {
	s := newTestServer(t)
	for _, path := range []string{"/probe", "/log", "/nothing-here"} {
		if refs := externalRefs(body(t, get(t, s, path, false))); len(refs) > 0 {
			t.Errorf("%s references another origin: %v", path, refs)
		}
	}
}

// The probe page is held to the stricter rule: not one absolute URL anywhere in
// the document, in an attribute or in the prose. That is why the control which
// moves a reader between the two listeners is a relative link to /switch and not
// a link to the other scheme — the absolute address lives in a Location header.
func TestProbePageContainsNoAbsoluteURLAtAll(t *testing.T) {
	s := newTestServer(t)
	page := body(t, get(t, s, "/probe", true))
	if m := absolutePattern.FindString(page); m != "" {
		t.Fatalf("the probe page contains an absolute URL near %q", excerpt(page, m))
	}

	// The negative control. Without it this assertion is a line that has never
	// been red, and a scanner that cannot be shown failing proves nothing about
	// the page it scans.
	sabotaged := strings.Replace(page, "<ol class=\"findings\">",
		`<img src="https://fonts.googleapis.com/css2?family=Inter"><ol class="findings">`, 1)
	if absolutePattern.FindString(sabotaged) == "" {
		t.Fatal("the scanner did not flag a deliberately injected external stylesheet, so it is not checking anything")
	}
	if len(externalRefs(sabotaged)) != 1 {
		t.Fatal("the reference scanner did not flag a deliberately injected external image")
	}
}

func excerpt(page, needle string) string {
	i := strings.Index(page, needle)
	start := max(i-40, 0)
	end := min(i+60, len(page))
	return page[start:end]
}

// ─── what the page answers with JavaScript switched off ──────────────────────

func TestProbeAnswersWithoutJavaScript(t *testing.T) {
	s := newTestServer(t)
	page := body(t, get(t, s, "/probe", true))

	required := map[string]string{
		"the scheme, in the largest type on the page": "HTTPS",
		"the user agent as the server saw it":         "CaptiveNetworkSupport",
		"the cookie round trip, answered server-side": "no cookie came back",
		"the CSS-only dark-scheme readout":            "dark-only",
		"the CSS-only viewport ladder":                "wb-0",
		"the webfont test, which is CSS only":         "NOT LOADED",
		"the current RFC 8908 answer":                 "captive: true",
		"a viewport meta with viewport-fit=cover":     "viewport-fit=cover",
		"the safe-area inset strips":                  "insetbar",
		"the certificate fingerprint on screen":       s.tlsm.leaf.NotAfter.Format("2006-01-02"),
	}
	for why, want := range required {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not carry %s (looked for %q)", why, want)
		}
	}

	// Nothing may be linked. One document, one request: a second request is one
	// the CNA may not make, and a page that renders half of itself is a
	// measurement of the wrong thing.
	if strings.Contains(page, `<link rel="stylesheet"`) || strings.Contains(page, `<script src=`) {
		t.Error("the page links an asset instead of inlining it")
	}
}

func TestProbeSaysSoWhenItIsBeingReadOnTheLaptop(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.RemoteAddr = "127.0.0.1:52001"
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "This is the laptop, not a phone") {
		t.Fatal("a page read over loopback must say that it settles nothing")
	}
}

// ─── the log renders client-controlled text ──────────────────────────────────

func TestLogEscapesAHostileUserAgent(t *testing.T) {
	s := newTestServer(t)
	s.log.add(event{
		Kind:   "http",
		Peer:   "192.0.2.31:51000",
		Detail: `GET /probe -> 200`,
		UA:     `<script>alert(1)</script>`,
	})

	page := body(t, get(t, s, "/log", false))
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Fatal("a User-Agent reached the log page unescaped; it is client-controlled text")
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Fatal("the hostile User-Agent is not on the page at all, escaped or otherwise")
	}
}

// ─── the RFC 8908 endpoint ───────────────────────────────────────────────────

func TestCaptiveAPIMediaTypeIsExact(t *testing.T) {
	s := newTestServer(t)
	resp := get(t, s, "/captive-portal/api", true)

	// A client that asks for the Captive Portal API and is answered
	// application/json treats the response as a failure. The resulting blank
	// screen is indistinguishable from a TLS refusal, which is the confusion this
	// whole kit is built to remove — so the media type is asserted exactly.
	if got := resp.Header.Get("Content-Type"); got != "application/captive+json" {
		t.Fatalf("Content-Type is %q, want application/captive+json", got)
	}
	if b := body(t, resp); !strings.Contains(b, `"captive":true`) {
		t.Fatalf("body does not report captivity: %s", b)
	}
}

func TestCaptiveToggleIsANavigation(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/captive", strings.NewReader("state=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)

	// 303 rather than a rendered page: the CNA re-checks captivity only after a
	// full navigation, so the control that flips the answer has to produce one.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("toggling captivity answered %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/probe" {
		t.Fatalf("Location is %q, want /probe", loc)
	}
	if s.captiveState() {
		t.Fatal("the toggle did not take effect")
	}
	if b := body(t, get(t, s, "/captive-portal/api", true)); !strings.Contains(b, `"captive":false`) {
		t.Fatalf("the API still reports captivity: %s", b)
	}
}

func TestConnectivityProbesFollowTheCaptiveState(t *testing.T) {
	s := newTestServer(t)

	resp := get(t, s, "/hotspot-detect.html", false)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("while captive, Apple's probe answered %d, want a redirect", resp.StatusCode)
	}

	s.setCaptive(false)
	if b := body(t, get(t, s, "/hotspot-detect.html", false)); !strings.Contains(b, "<TITLE>Success</TITLE>") {
		t.Fatalf("once released, Apple's probe must get the exact success document, got %q", b)
	}
	if code := get(t, s, "/generate_204", false).StatusCode; code != http.StatusNoContent {
		t.Fatalf("once released, Android's probe answered %d, want 204", code)
	}
}

func TestSwitchKeepsTheAbsoluteAddressInAHeader(t *testing.T) {
	s := newTestServer(t)

	resp := get(t, s, "/switch", false)
	if got := resp.Header.Get("Location"); got != "https://192.0.2.23:8443/probe" {
		t.Fatalf("from HTTP, /switch pointed at %q", got)
	}
	resp = get(t, s, "/switch", true)
	if got := resp.Header.Get("Location"); got != "http://192.0.2.23:8080/probe" {
		t.Fatalf("from HTTPS, /switch pointed at %q", got)
	}
}

func TestRootCertificateIsOfferedForInstalling(t *testing.T) {
	s := newTestServer(t)
	resp := get(t, s, "/ca.crt", false)

	// This media type is what makes iOS offer to install the certificate rather
	// than render it as text. Without it the fallback branch of the HTTPS question cannot be
	// walked on the device at all.
	if got := resp.Header.Get("Content-Type"); got != "application/x-x509-ca-cert" {
		t.Fatalf("Content-Type is %q, want application/x-x509-ca-cert", got)
	}
	if b := body(t, resp); !strings.Contains(b, "BEGIN CERTIFICATE") {
		t.Fatal("the root certificate is not PEM")
	}
}

func TestFontsAreServedWithTheirOwnMediaTypes(t *testing.T) {
	s := newTestServer(t)
	for path, want := range map[string]string{
		"/probe-font.ttf":  "font/ttf",
		"/probe-font.woff": "font/woff",
	} {
		resp := get(t, s, path, false)
		if got := resp.Header.Get("Content-Type"); got != want {
			t.Errorf("%s served as %q, want %q", path, got, want)
		}
		if len(body(t, resp)) == 0 {
			t.Errorf("%s served nothing", path)
		}
	}
}
