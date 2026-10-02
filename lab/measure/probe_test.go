package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── the portal's probe, served here ─────────────────────────────────────────
//
// The device walk needs one page that reports what a phone's browser does —
// storage, viewport and colour scheme, and the HTTPS question are lost without
// it. It is built by the portal
// (`web/portal/build/build.ts` → `dist-probe/`) and served here, at
// `/{lang}/probe/`, because this is the host that owns `POST /report`.
//
// **The wiring is the thing under test, not the document.** The document's
// contents are held by `web/portal/src/probe.test.ts`; what nothing else can
// check is that a digest sent by a phone reaches a log line rather than a 404,
// and that the page is not reachable anywhere a guest can reach.

// A stand-in for the built output. The real file is ~29 kB of inlined CSS and
// script; what matters here is that whatever bytes the build produced are the
// bytes the phone gets, unmodified.
func withProbeDir(t *testing.T, s *server, docs map[string]string) *server {
	t.Helper()
	dir := t.TempDir()
	for lang, doc := range docs {
		p := filepath.Join(dir, lang, "probe", "index.html")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.probeDir = dir
	return s
}

func TestThePortalProbeIsServedInEveryLanguageThePortalCarries(t *testing.T) {
	s := withProbeDir(t, newTestServer(t), map[string]string{
		"en": "<!doctype html><html lang=\"en\"><body>EN PROBE</body></html>",
		"vi": "<!doctype html><html lang=\"vi\"><body>VI PROBE</body></html>",
	})

	for _, lang := range portalProbeLangs {
		for _, path := range []string{"/" + lang + "/probe", "/" + lang + "/probe/"} {
			// A person types this on a phone keyboard, and a CNA re-checks
			// captivity only after a full navigation — so both spellings have to
			// answer, not just the tidy one.
			resp := get(t, s, path, false)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s answered %d, want 200", path, resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("GET %s: Content-Type = %q, want text/html", path, ct)
			}
			// A cached probe document is a probe of whatever was measured last
			// time, which is worse than no document at all.
			if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
				t.Errorf("GET %s: Cache-Control = %q, want no-store", path, cc)
			}
			if want := strings.ToUpper(lang) + " PROBE"; !strings.Contains(body(t, resp), want) {
				t.Errorf("GET %s did not serve the %s document", path, lang)
			}
		}
	}
}

func TestThePortalProbeIsNotReachableWhenNoDirectoryIsNamed(t *testing.T) {
	// The default is that it is off. A diagnostic being one flag away is the
	// point; it being on because someone ran the program once is not.
	s := newTestServer(t)

	for _, lang := range portalProbeLangs {
		resp := get(t, s, "/"+lang+"/probe/", false)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET /%s/probe/ with no -probe answered %d, want 404", lang, resp.StatusCode)
		}
		// And it names the flag, because a 404 during a measurement looks exactly
		// like the CNA refusing a certificate.
		if !strings.Contains(body(t, resp), "-probe") {
			t.Errorf("GET /%s/probe/: the 404 does not name the flag that serves it", lang)
		}
	}
}

func TestThePortalProbeAnswersInTheLanguageOnly(t *testing.T) {
	// The language prefix is a lookup key, never a path segment, so nothing in a
	// request reaches outside the directory -probe named.
	s := withProbeDir(t, newTestServer(t), map[string]string{
		"en": "<!doctype html><html lang=\"en\"><body>EN PROBE</body></html>",
		"vi": "<!doctype html><html lang=\"vi\"><body>VI PROBE</body></html>",
	})

	for _, path := range []string{
		"/de/probe/",             // a language the build does not carry
		"/en/probe/../../../etc", // traversal, as a browser would send it
		"/en/probe/index.html",   // the file, not the route
		"/en/probe/en/probe/",    // the prefix twice
	} {
		resp := get(t, s, path, false)
		if resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s answered 200; the probe is reachable at a path it does not own", path)
		}
	}
}

func TestThePortalProbeDigestLandsInTheLog(t *testing.T) {
	// **The whole point of the page.** A digest that reaches a 404 is a
	// measurement that goes back to being a photograph, and cropped photographs
	// are the reason the portal's own probe exists.
	s := withProbeDir(t, newTestServer(t), map[string]string{
		"en": "<!doctype html><html lang=\"en\"><body>EN PROBE</body></html>",
		"vi": "<!doctype html><html lang=\"vi\"><body>VI PROBE</body></html>",
	})

	// What the page sends: machine tokens, the same names as its rows.
	const digest = "vp=390x844  dpr=3  screen=390x844  insets=0/0/34/0  ls=ok  ss=ok  font=ttf=applied"

	req := httptest.NewRequest(http.MethodPost, "/report", strings.NewReader(digest))
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /report answered %d, want 204", rec.Code)
	}

	events := s.log.events()
	found := false
	for _, e := range events {
		if e.Kind == "report" && strings.Contains(e.Detail, "vp=390x844") && strings.Contains(e.Detail, "ls=ok") {
			found = true
		}
	}
	if !found {
		t.Errorf("the digest never reached the log; recorded: %+v", events)
	}
}

func TestThePortalProbeWinsOverTheProxy(t *testing.T) {
	// With -proxy in play this host *is* the portal's origin as far as a browser
	// is concerned, which is what makes same-origin `/report` true. The catch-all
	// would otherwise swallow the probe and forward it to the portal, which has
	// no such document.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()

	s := withProbeDir(t, newTestServer(t), map[string]string{
		"en": "<!doctype html><html lang=\"en\"><body>EN PROBE</body></html>",
		"vi": "<!doctype html><html lang=\"vi\"><body>VI PROBE</body></html>",
	})
	p, err := newProxy(upstream.URL, s.log)
	if err != nil {
		t.Fatal(err)
	}
	s.proxy = p

	resp := get(t, s, "/en/probe/", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /en/probe/ behind -proxy answered %d, want the probe, not the upstream", resp.StatusCode)
	}

	// And everything else still goes to the portal, which is the arrangement's
	// whole reason for existing.
	if proxied := get(t, s, "/en/", false); proxied.StatusCode != http.StatusTeapot {
		t.Errorf("GET /en/ answered %d, want it proxied to the portal", proxied.StatusCode)
	}
}
