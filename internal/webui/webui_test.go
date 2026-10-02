package webui

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kiennt048/foyer/internal/portal"
)

// ── the fixture world, and why most of this file does not skip ───────────────
//
// `docs/rules/testing.md` allows a skip whose reason a test can **evaluate**,
// and `!Built()` is one — it stops being true the moment somebody runs
// `make web-embed`. But a skip is still absent coverage, and on a fresh clone
// `Built()` is false, so gating this whole file on it would mean the routing
// contract — the traversal table, the SPA fallback, the `/api/` carve-out, the
// anti-loop invariant — is checked on nobody's machine until they happen to have
// built the frontend.
//
// So the split is: **behaviour is tested against a synthetic tree and never
// skips**, and only the assertions that are genuinely *about the shipped
// artefacts* — that the real documents are documents, that every asset the console
// references is embedded, that every extension is in the media-type table — depend
// on `Built()`.

// stubDocument is the shape of `web/portal/dist/index.html`: the build's static
// best-effort root, a script and a `<meta http-equiv="refresh">` and an empty body.
//
// It is here because it is the **defect**, kept in the harness so the assertions
// below can be re-pointed at it on every run. In a different spelling it is also
// what a host with no embedded frontend serves: a `302` with a 26-byte body,
// forever.
const stubDocument = `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
	`<script>location.replace("/en/"+location.search)</script>` +
	`<meta http-equiv="refresh" content="0;url=/en/">` +
	`<title>Foyer</title></head><body></body></html>`

// realDocument is the shape of a built portal document, reduced to the properties
// `notADocument` is entitled to demand: a doctype, a language, an inlined
// stylesheet, a body with something in it, and no meta refresh.
func realDocument(lang, marker string) string {
	return "<!doctype html>\n<html lang=\"" + lang + "\">\n<head>\n" +
		"<meta charset=\"utf-8\">\n<title>Get online · Foyer</title>\n" +
		"<style>" + strings.Repeat(":root{--x:0}", 120) + "</style>\n</head>\n<body>\n" +
		"<div class=\"shell\"><h1>" + marker + "</h1>" +
		strings.Repeat("<p>Signing in here is a billing and consent boundary.</p>", 12) +
		"</div>\n</body>\n</html>\n"
}

func portalFixture() *portalTree {
	return newPortalTree(fstest.MapFS{
		"index.html":              {Data: []byte(stubDocument)},
		"en/index.html":           {Data: []byte(realDocument("en", "Get online"))},
		"en/voucher/index.html":   {Data: []byte(realDocument("en", "Voucher"))},
		"en/connected/index.html": {Data: []byte(realDocument("en", "Connected"))},
		"vi/index.html":           {Data: []byte(realDocument("vi", "Truy cập"))},
		"vi/voucher/index.html":   {Data: []byte(realDocument("vi", "Mã"))},
		// The two `all:` embeds a workstation or a placeholder can put in the tree.
		".gitkeep":         {Data: []byte("")},
		".DS_Store":        {Data: []byte("\x00\x01secrets")},
		"_notes/plan.html": {Data: []byte(realDocument("en", "internal"))},
	})
}

func adminFixture() *adminTree {
	return newAdminTree(fstest.MapFS{
		"index.html": {Data: []byte(`<!doctype html><html lang="en"><head>` +
			`<script type="module" crossorigin src="/assets/index-AAAA1111.js"></script>` +
			`<link rel="stylesheet" crossorigin href="/assets/index-BBBB2222.css">` +
			`</head><body><div id="root"></div></body></html>`)},
		"assets/index-AAAA1111.js":  {Data: []byte("export const a=1;")},
		"assets/index-BBBB2222.css": {Data: []byte(":root{--x:0}")},
		".gitkeep":                  {Data: []byte("")},
	})
}

func get(t *testing.T, h http.Handler, target string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Result()
}

// ── the assertion the whole package exists for ───────────────────────────────

// notADocument returns why body is not a portal document, or nil.
//
// **Every clause here is aimed at a body that a status check calls a success.** The
// shipped defect answered `302` with 26 bytes; the plausible next one answers `200`
// with a placeholder. `assert 200` cannot tell any of those from a working portal,
// which is precisely why `cmd/foyer/listeners_test.go` and
// `internal/portal/entry_test.go` were both green while the deployment served no
// HTML at all.
//
// The clause that does the most work is the last one: **a meta refresh is a
// redirect wearing a document's clothes**, and it is the exact shape of the build's
// root stub.
func notADocument(lang string, body []byte) error {
	s := string(body)
	low := strings.ToLower(s)
	switch {
	case len(body) == 0:
		return errf("empty body")
	case !strings.HasPrefix(low, "<!doctype html"):
		return errf("no doctype; starts %q", first(s, 40))
	case !strings.Contains(low, `<html lang="`+strings.ToLower(lang)+`"`):
		return errf("no <html lang=%q>", lang)
	case !strings.Contains(low, "</html>"):
		return errf("no closing </html>")
	case strings.Contains(low, "http-equiv=\"refresh\"") || strings.Contains(low, "http-equiv=refresh"):
		return errf("a meta refresh: this is a redirect, not a document")
	case !strings.Contains(low, "<style>"):
		// The portal inlines its stylesheet into every document — one HTTP request
		// per document, because Android clears the cache on every portal launch.
		// A document with no `<style>` is not a portal document; it is a stub.
		return errf("no inlined <style>: the portal inlines its CSS into every document")
	case len(bodyElement(low)) < 500:
		return errf("<body> holds %d bytes; a rendered page is not that small", len(bodyElement(low)))
	case len(body) < 2000:
		return errf("%d bytes total; the smallest built document is over 11 kB", len(body))
	}
	return nil
}

func bodyElement(low string) string {
	i := strings.Index(low, "<body")
	j := strings.LastIndex(low, "</body>")
	if i < 0 || j < 0 || j <= i {
		return ""
	}
	return low[i:j]
}

func errf(format string, args ...any) error { return fmt.Errorf(format, args...) }

func read(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return b
}

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// TestTheDocumentAssertionCanFail re-injects the defect on every run.
//
// The failure this guards against is **an assertion aimed somewhere the subject
// does not appear** (`docs/rules/testing.md`, Principles), whose tell is that
// repairing the defect changes nothing. The way to know `notADocument` is pointed at the right
// surface is to point it at the wrong bodies and require it to complain — the
// deployment's 26-byte redirect, the build's root stub, an empty body, a `200` with
// a plausible placeholder in it.
//
// ── every case names the clause that must catch it, and that is not pedantry ─
//
// Asserting only *"an error came back"* lets a sabotage that deletes the
// meta-refresh clause outright **stay green** — because the root stub also has an
// empty `<body>` and is under 2 kB, so a later clause catches it and the test
// cannot tell which. That is `docs/rules/database.md`'s finding one file over: *"a
// negative test whose subject has more than one way to fail is not yet a test — it
// is a test of whichever guard happens to fire first, and you do not find out which
// until you remove one."*
//
// So each body below is built from a **valid** document with exactly one property
// broken, and the assertion is on the reason. Deleting any single clause from
// `notADocument` now turns exactly one of these red, by name.
func TestTheDocumentAssertionCanFail(t *testing.T) {
	valid := realDocument("en", "Get online")
	for _, tc := range []struct{ name, body, want string }{
		// One clause at a time, against an otherwise-valid document.
		{"no doctype", strings.TrimPrefix(valid, "<!doctype html>"), "no doctype"},
		{"the wrong language", strings.Replace(valid, `lang="en"`, `lang="de"`, 1), "no <html lang="},
		{"never closed", strings.Replace(valid, "</html>", "</htmI>", 1), "no closing </html>"},
		{"a meta refresh in an otherwise real document",
			strings.Replace(valid, "<head>", `<head><meta http-equiv="refresh" content="0;url=/en/">`, 1),
			"a meta refresh"},
		{"no inlined stylesheet", strings.Replace(valid, "<style>", "<styl3>", 1), "inlined <style>"},
		{"a body with nothing in it", "<!doctype html><html lang=\"en\"><head><style>" +
			strings.Repeat("x", 3000) + "</style></head><body></body></html>", "<body> holds"},
		{"too small to be a rendered page", "<!doctype html><html lang=\"en\"><head><style>x</style>" +
			"</head><body>" + strings.Repeat("y", 600) + "</body></html>", "bytes total"},

		// And the three bodies this is actually written against.
		{"the body of the entry redirect loop", `<a href="/en/">Found</a>.`, "no doctype"},
		{"the build's own root stub", stubDocument, "a meta refresh"},
		{"an empty body", "", "empty body"},
		{"an admin document, which is not a portal document", `<!doctype html><html lang="en">` +
			`<head><title>Foyer</title></head><body><div id="root"></div></body></html>`,
			"inlined <style>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := notADocument("en", []byte(tc.body))
			if err == nil {
				t.Fatalf("accepted as a document — notADocument cannot fail, so every "+
					"assertion resting on it is decoration (%s)", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refused for the wrong reason:\n got %q\nwant a reason naming %q\n"+
					"— a body with more than one way to fail proves only whichever clause fired first",
					err.Error(), tc.want)
			}
		})
	}

	// And the other half, or the clauses above are satisfied by a predicate that
	// rejects everything.
	if err := notADocument("en", []byte(realDocument("en", "Get online"))); err != nil {
		t.Fatalf("a real document was rejected: %v", err)
	}
	if err := notADocument("vi", []byte(realDocument("vi", "Truy cập"))); err != nil {
		t.Fatalf("a real Vietnamese document was rejected: %v", err)
	}
	// A document in the wrong language is not this route's document. Without this
	// the language check is satisfiable by serving `en` everywhere.
	if err := notADocument("vi", []byte(realDocument("en", "Get online"))); err == nil {
		t.Fatal("an English document was accepted for the Vietnamese route")
	}
}

// ── the portal ───────────────────────────────────────────────────────────────

// TestThePortalServesADocumentForEveryRouteItCarries is the test nobody wrote.
//
// The front door was covered from both sides — `listeners_test.go` and
// `entry_test.go` assert the `302` — and the question neither asked was *and then
// what*. It fetches every route the tree carries and asserts a document comes back,
// on content rather than on status.
func TestThePortalServesADocumentForEveryRouteItCarries(t *testing.T) {
	for _, tree := range []struct {
		name string
		tree *portalTree
	}{
		{"the synthetic tree", portalFixture()},
		{"the embedded tree", thePortal},
	} {
		t.Run(tree.name, func(t *testing.T) {
			if !tree.tree.built() {
				// An evaluable condition, not a phase: it stops being true the
				// moment `make web-embed` runs. The synthetic sibling above keeps
				// the assertion itself exercised meanwhile.
				t.Skip("no portal documents are embedded in this binary (run `make web-embed`)")
			}
			paths := sortedRoutes(tree.tree.routes)
			if len(paths) == 0 {
				t.Fatal("no routes at all")
			}
			for _, p := range paths {
				lang := strings.Split(strings.Trim(p, "/"), "/")[0]
				resp := get(t, tree.tree, p)
				if resp.StatusCode != http.StatusOK {
					t.Errorf("%s: status %d, want 200", p, resp.StatusCode)
					continue
				}
				if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
					t.Errorf("%s: Content-Type %q", p, ct)
				}
				body := read(t, resp)
				if err := notADocument(lang, body); err != nil {
					t.Errorf("%s: %v", p, err)
				}
			}
			t.Logf("%d documents served", len(paths))
		})
	}
}

// TestTheEntryRedirectTargetIsAlwaysADocument closes the loop between the two
// halves that were each correct on their own.
//
// It does not restate the redirect rule — it calls the **same exported function**
// `EntryHandler` calls, `portal.NegotiateLanguage`, and requires every answer it can
// give to be a path this package will serve. That is the derivation
// `docs/rules/testing.md` asks for: the stand-in is the authority itself.
func TestTheEntryRedirectTargetIsAlwaysADocument(t *testing.T) {
	tree := portalFixture()
	advertised := languagesOf(tree)
	if len(advertised) < 2 {
		t.Fatalf("the fixture must carry more than one language to test negotiation, has %v", advertised)
	}

	for _, accept := range []string{
		"", "en", "vi", "en-GB,en;q=0.9", "vi-VN,vi;q=0.9,en;q=0.8",
		"fr-FR,fr;q=0.9", "*", "garbage", "en;q=0.1,vi;q=0.9", ",,,", "zh-Hans-CN",
	} {
		lang := portal.NegotiateLanguage(accept, advertised, advertised[0])
		target := "/" + lang + "/"
		a, redirect, ok := tree.resolve(target)
		if !ok || redirect != "" || a == nil {
			t.Fatalf("Accept-Language %q negotiated to %q, and %s is not a document — "+
				"this is the entry redirect loop", accept, lang, target)
		}
		if err := notADocument(lang, a.body); err != nil {
			t.Fatalf("Accept-Language %q → %s: %v", accept, target, err)
		}
	}
}

// TestASiteAdvertisingALanguageThePortalCannotServeGets404AndNotALoop records a
// hazard rather than a defect, and the distinction matters.
//
// `EntryHandler` negotiates against the **site's advertised** languages, which come
// from the database, while the documents come from the frontend build. Nothing makes
// those two sets agree, so an operator who advertises a language the build does not
// carry sends every guest to a path with no document behind it.
//
// What this asserts is the property that keeps that survivable: it is a `404`, and
// **never a redirect back to `/`**. A 404 is a broken venue configuration somebody
// can see and fix; a redirect to `/` is the infinite loop, and it looks like a
// network fault. [PortalLanguages] exists so a caller can intersect and avoid both.
func TestASiteAdvertisingALanguageThePortalCannotServeGets404AndNotALoop(t *testing.T) {
	tree := portalFixture()
	lang := portal.NegotiateLanguage("fr-FR,fr;q=0.9", []string{"fr"}, "fr")
	if lang != "fr" {
		t.Fatalf("negotiated %q, expected the advertised language to win", lang)
	}
	resp := get(t, tree, "/fr/")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/fr/ answered %d, want 404", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		t.Fatalf("/fr/ answered a redirect to %q — that is the loop", loc)
	}
}

// TestThePortalNeverRedirectsToTheRootOrToItself forbids the redirect loop
// directly, over every route the tree carries and a pile of paths it does not.
//
// An `/en/` answering `302 Location: /en/` is invisible to an assertion about
// whether the `302` is *correct*; this one asks where the second one goes.
func TestThePortalNeverRedirectsToTheRootOrToItself(t *testing.T) {
	tree := portalFixture()
	targets := append(sortedRoutes(tree.routes),
		"/", "/en", "/vi", "/en/voucher", "/nope", "/en/nope/", "/api/v0/portal/context",
		"/.well-known/captive-portal", "/en/../en/", "//en//",
	)
	for _, target := range targets {
		resp := get(t, tree, target)
		loc := resp.Header.Get("Location")
		switch {
		case loc == "":
		case loc == "/" || strings.HasPrefix(loc, "/?"):
			t.Errorf("%s redirected to the root — that is the loop", target)
		case loc == target || strings.HasPrefix(loc, target+"?"):
			t.Errorf("%s redirected to itself — that is the loop", target)
		}
	}
}

// TestTheRootStubIsNeverServed.
//
// `web/portal/dist/index.html` is a `<meta http-equiv="refresh">` that necessarily
// drops the query string, and `build.ts` names the Go binary as its replacement.
// Serving it would silently discard the MAC and the AP on every arrival, and would
// look exactly like a working portal.
func TestTheRootStubIsNeverServed(t *testing.T) {
	for _, tc := range []struct {
		name string
		tree *portalTree
	}{{"the synthetic tree", portalFixture()}, {"the embedded tree", thePortal}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.tree.routes["/"]; ok {
				t.Fatal("`/` is a route: the static root stub can win a mux")
			}
			resp := get(t, tc.tree, "/")
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("`/` answered %d; it belongs to EntryHandler", resp.StatusCode)
			}
			if PortalHas("/") {
				t.Fatal("PortalHas(\"/\") is true: a caller will route the front door here")
			}
		})
	}
	// And the fixture really does carry the stub, or the assertion above is
	// satisfied by a tree that never had one.
	if _, err := fs.Stat(fstest.MapFS{"index.html": {Data: []byte(stubDocument)}}, "index.html"); err != nil {
		t.Fatal("the fixture no longer carries a root stub to drop")
	}
}

// TestPortalRelaysTheQueryStringOnTheCanonicalRedirect.
//
// The parameters are the only evidence `VerifyPresence` has (`security.md` §4) and
// the only source of the success page's return link. A redirect that drops them
// authenticates nobody and looks entirely healthy.
func TestPortalRelaysTheQueryStringOnTheCanonicalRedirect(t *testing.T) {
	tree := portalFixture()
	const q = "ap=aa:bb:cc:dd:ee:ff&id=11:22:33:44:55:66&t=1&ssid=Caf%C3%A9&url=http%3A%2F%2Fexample.com"
	resp := get(t, tree, "/en/voucher?"+q)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d, want 302", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/en/voucher/?"+q {
		t.Fatalf("Location %q\n     want %q — the NAS parameters must survive verbatim", loc, "/en/voucher/?"+q)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control %q on a redirect a CNA will re-follow", cc)
	}
	// A permanent redirect is cached by the user agent, and a captive portal is the
	// one place a client holding last week's routing decision cannot be told why it
	// is offline.
	if resp.StatusCode == http.StatusMovedPermanently {
		t.Fatal("301: a captive portal must not ask a client to remember a route")
	}
}

// TestPortalDocumentsAreNeverStored.
//
// Android calls `clearCache(true)` on every portal launch and the CNA's data store
// is ephemeral, so a cache header buys nothing on the two clients that matter — and
// what a cache *can* still do is hold a stale document in front of a repaired
// portal.
func TestPortalDocumentsAreNeverStored(t *testing.T) {
	tree := portalFixture()
	for _, p := range sortedRoutes(tree.routes) {
		if cc := get(t, tree, p).Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control %q, want no-store", p, cc)
		}
	}
}

// ── the console ──────────────────────────────────────────────────────────────

// TestAdminDeepLinksFallBackToTheDocument — a reload on `/sessions` must work.
func TestAdminDeepLinksFallBackToTheDocument(t *testing.T) {
	tree := adminFixture()
	for _, p := range []string{"/", "/sessions", "/nas/abc-123", "/vouchers/batch/9", "/a/b/c/d"} {
		resp := get(t, tree, p)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d, want the SPA fallback", p, resp.StatusCode)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type %q", p, ct)
		}
		if !strings.Contains(string(read(t, resp)), `id="root"`) {
			t.Errorf("%s: the fallback did not return the console document", p)
		}
	}
}

// TestTheAdminFallbackDoesNotSwallowApiOrAssets.
//
// Both halves matter and they fail differently. A mistyped API path answered
// `200 text/html` reaches the console as a JSON parse error — a support call about
// the wrong thing. A missing hashed asset answered `200 text/html` reaches the
// browser as a syntax error inside a page, and hides the half-copied tree that
// caused it.
func TestTheAdminFallbackDoesNotSwallowApiOrAssets(t *testing.T) {
	tree := adminFixture()
	for _, p := range []string{
		"/api", "/api/", "/api/v0/nas", "/api/v1/typo", "/api/v0/sessions/../x",
		"/assets/index-GONE0000.js", "/assets/", "/assets/nested/thing.css",
	} {
		resp := get(t, tree, p)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404 — the SPA fallback swallowed it", p, resp.StatusCode)
			continue
		}
		if strings.Contains(string(read(t, resp)), `id="root"`) {
			t.Errorf("%s: answered the console document", p)
		}
	}
	// The other half: a path that *is* a client-side route still gets the fallback,
	// or this test is satisfied by a handler that 404s everything.
	if get(t, tree, "/apiary").StatusCode != http.StatusOK {
		t.Fatal("/apiary is a client-side route and was refused: the prefix match is too wide")
	}
	if get(t, tree, "/assets/index-AAAA1111.js").StatusCode != http.StatusOK {
		t.Fatal("a real asset was refused")
	}
}

// TestAdminAssetsAreImmutableAndTheDocumentIsNot.
func TestAdminAssetsAreImmutableAndTheDocumentIsNot(t *testing.T) {
	tree := adminFixture()
	asset := get(t, tree, "/assets/index-AAAA1111.js")
	if cc := asset.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("a content-hashed asset answered Cache-Control %q", cc)
	}
	if ct := asset.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("a .js asset answered Content-Type %q", ct)
	}
	doc := get(t, tree, "/")
	if cc := doc.Header.Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("index.html answered %q: it is the mutable pointer at the hashed names", cc)
	}
	if doc.Header.Get("ETag") == "" {
		t.Error("no ETag on index.html, so `no-cache` has nothing to revalidate against")
	}
	// The ETag has to be a function of the bytes, or a redeploy serves the old
	// document forever.
	other := newAdminTree(fstest.MapFS{"index.html": {Data: []byte("<!doctype html><html><body>x</body></html>")}})
	if other.index.etag == tree.index.etag {
		t.Error("two different documents share an ETag")
	}
}

// TestEveryAssetTheConsoleReferencesIsEmbedded is the half-copied-tree detector.
//
// It reads `index.html`'s own `src` and `href` attributes and requires each to be
// served — so a build step that copies over the top and leaves a stale hash, or one
// that drops `assets/` entirely, is red here rather than a white screen with a
// console error.
func TestEveryAssetTheConsoleReferencesIsEmbedded(t *testing.T) {
	ref := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)
	for _, tc := range []struct {
		name string
		tree *adminTree
	}{{"the synthetic tree", adminFixture()}, {"the embedded tree", theAdmin}} {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.tree.built() {
				t.Skip("no console document is embedded in this binary (run `make web-embed`)")
			}
			found := ref.FindAllStringSubmatch(string(tc.tree.index.body), -1)
			if len(found) == 0 {
				t.Fatal("index.html references no assets at all; the build shape changed")
			}
			for _, m := range found {
				resp := get(t, tc.tree, m[1])
				if resp.StatusCode != http.StatusOK {
					t.Errorf("%s is referenced by index.html and answered %d — "+
						"the embedded tree is stale or half copied", m[1], resp.StatusCode)
				}
			}
			t.Logf("%d referenced assets, all embedded", len(found))
		})
	}
}

// ── traversal ────────────────────────────────────────────────────────────────

// TestNoPathEscapesTheEmbeddedTree.
//
// The defence is structural — a request path is only ever a map key, and there is no
// `Open(userInput)` in this package — but a structural argument nobody re-checks is
// a comment, so the table is driven.
func TestNoPathEscapesTheEmbeddedTree(t *testing.T) {
	pt, at := portalFixture(), adminFixture()
	for _, p := range []string{
		"/../webui.go", "/../../go.mod", "/en/../../etc/passwd", "/%2e%2e/webui.go",
		"/....//webui.go", "/en/%2e%2e%2f%2e%2e%2fgo.mod", `/..\..\go.mod`,
		"/./././../portal.go", "//../go.mod", "/en/voucher/../../../../go.mod",
	} {
		for name, h := range map[string]http.Handler{"portal": pt, "admin": at} {
			resp := get(t, h, p)
			body := string(read(t, resp))
			if strings.Contains(body, "package webui") || strings.Contains(body, "module github.com") {
				t.Fatalf("%s served source for %q", name, p)
			}
			if name == "portal" && resp.StatusCode == http.StatusOK {
				t.Errorf("portal answered 200 for %q", p)
			}
		}
	}
	// The admin's SPA fallback answers 200 for most of those by design — it is a
	// client-side route as far as it knows. What matters is that it is the console
	// document and never a file from outside the tree, which the body check above
	// covers.
	if got := string(read(t, get(t, at, "/../../go.mod"))); !strings.Contains(got, `id="root"`) {
		t.Fatalf("the admin answered something other than the console document: %q", first(got, 80))
	}
}

// TestDotAndUnderscoreSegmentsAreRefused — `all:` puts `.gitkeep` and a
// workstation's `.DS_Store` in the embedded tree. Neither may be a URL.
func TestDotAndUnderscoreSegmentsAreRefused(t *testing.T) {
	pt, at := portalFixture(), adminFixture()
	// The fixture carries them, so this is not vacuous.
	for _, p := range []string{"/.gitkeep", "/.DS_Store", "/_notes/plan.html", "/en/.gitkeep"} {
		if _, _, ok := pt.resolve(p); ok {
			t.Errorf("portal resolves %q", p)
		}
		if resp := get(t, pt, p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("portal answered %d for %q", resp.StatusCode, p)
		}
		if PortalHas(p) {
			t.Errorf("PortalHas(%q) is true", p)
		}
	}
	if resp := get(t, at, "/.gitkeep"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("admin answered %d for /.gitkeep", resp.StatusCode)
	}
	// And they are not in the route table in the first place — two gates, because
	// the route table is what a future reader will trust.
	for _, r := range []routes{pt.routes, at.routes} {
		for k := range r {
			for _, seg := range strings.Split(k, "/") {
				if strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "_") {
					t.Errorf("route table carries %q", k)
				}
			}
		}
	}
}

// ── the unbuilt binary ───────────────────────────────────────────────────────

// TestAnUnbuiltBinarySaysSoLoudly.
//
// `internal/version` is the shape: a build that cannot name itself must say so,
// because *"the silence is the failure mode"*. A `200` with an empty body would look
// to every automated check like a working portal.
func TestAnUnbuiltBinarySaysSoLoudly(t *testing.T) {
	for _, tc := range []struct {
		name string
		fsys fs.FS
	}{
		{"an entirely absent tree", emptyFS{}},
		{"nothing but the committed placeholder", fstest.MapFS{".gitkeep": {Data: []byte("")}}},
		{"a workstation's leftovers and nothing else", fstest.MapFS{
			".DS_Store": {Data: []byte("x")}, "_old/index.html": {Data: []byte(stubDocument)}}},
		{"the root stub alone — the static build with no documents behind it",
			fstest.MapFS{"index.html": {Data: []byte(stubDocument)}}},
		// ── the world the cases above cannot express ─────────────────────────
		//
		// Without this case a sabotage **stays green**: replacing
		// `portalTree.built()` with `len(t.routes) > 0` is caught by none of the
		// cases above, since every one of them leaves the map empty. This is the
		// only shape that separates the two — sub-pages with no landing behind
		// them, which is what a partial sync or a renamed page produces.
		//
		// `len > 0` calls it built; `EntryHandler` then redirects to `/en/`, and
		// `/en/` is a 404. `docs/rules/testing.md`: a world that cannot express
		// a property makes every check of that property vacuous.
		{"sub-pages with no language landing behind them", fstest.MapFS{
			"en/voucher/index.html":   {Data: []byte(realDocument("en", "Voucher"))},
			"en/connected/index.html": {Data: []byte(realDocument("en", "Connected"))},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if p := newPortalTree(tc.fsys); p.built() {
				t.Error("portalTree.built() is true with no documents in it")
			}
			if a := newAdminTree(tc.fsys); a.built() {
				t.Error("adminTree.built() is true with no console document in it")
			}
		})
	}

	// The handler an unbuilt binary answers with. Constructed directly rather than
	// through `Portal()`, so this runs on a machine where the frontend *is* built.
	emptyP, emptyA := newPortalTree(emptyFS{}), newAdminTree(emptyFS{})
	h := absent{which: "portal", state: describe(emptyP, emptyA), remedy: remedy(emptyP, emptyA)}
	resp := get(t, h, "/en/")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503 — a 200 with no UI is indistinguishable from a working portal", resp.StatusCode)
	}
	if resp.Header.Get("X-Foyer-UI") != "absent" {
		t.Error("no X-Foyer-UI: absent header for anything reading headers")
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q: a cache would pin the failure in front of a repaired deployment", cc)
	}
	body := string(read(t, resp))
	for _, want := range []string{"web-embed", "portal-dist", "admin-dist", "build defect"} {
		if !strings.Contains(body, want) {
			t.Errorf("the diagnostic body does not mention %q; the person reading it is the person who can fix it", want)
		}
	}
	// It names *which* half is missing, so a half-copied build is diagnosable from the
	// response body alone — which is all an operator has when the portal 503s.
	halfA := absent{which: "portal", state: describe(newPortalTree(emptyFS{}), adminFixture())}
	if !strings.Contains(halfA.body(), "portal-dist holds no documents") ||
		!strings.Contains(halfA.body(), "admin-dist is complete") {
		t.Errorf("a half-built binary does not name the missing half:\n%s", halfA.body())
	}
}

// TestAConsoleDocumentWithNoApplicationBehindItIsNotBuilt.
//
// An `adminTree.built()` that asked only whether `index.html` existed would be
// wrong: `index.html` is 440 bytes that carry no application at all — so a tree
// with the document and no `assets/` would report *built*, answer `200
// text/html`, and render a white screen with a 404 in the browser console.
// That is the exact output of a sync that lands one directory and not the other, or
// that copies over the top and leaves a stale hash when the entry chunk is rebuilt.
//
// The three cases below are the three ways it happens, and each is red against a
// `built()` that checks only the document.
func TestAConsoleDocumentWithNoApplicationBehindItIsNotBuilt(t *testing.T) {
	const doc = `<!doctype html><html lang="en"><head>` +
		`<script type="module" src="/assets/index-AAAA1111.js"></script>` +
		`<link rel="stylesheet" href="/assets/index-BBBB2222.css">` +
		`</head><body><div id="root"></div></body></html>`

	for _, tc := range []struct {
		name string
		fsys fstest.MapFS
		want string
	}{
		{"assets/ never landed", fstest.MapFS{
			"index.html": {Data: []byte(doc)},
		}, "/assets/index-AAAA1111.js"},
		{"a stale hash left behind by a copy that did not delete", fstest.MapFS{
			"index.html":                {Data: []byte(doc)},
			"assets/index-OLD00000.js":  {Data: []byte("export const a=1;")},
			"assets/index-BBBB2222.css": {Data: []byte(":root{--x:0}")},
		}, "/assets/index-AAAA1111.js"},
		{"the stylesheet alone went missing", fstest.MapFS{
			"index.html":               {Data: []byte(doc)},
			"assets/index-AAAA1111.js": {Data: []byte("export const a=1;")},
		}, "/assets/index-BBBB2222.css"},
		{"an index.html that is not a console document", fstest.MapFS{
			"index.html": {Data: []byte(stubDocument)},
		}, "references no assets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := newAdminTree(tc.fsys)
			if tree.built() {
				t.Fatal("built() is true: the console would serve a white screen and report success")
			}
			body := absent{which: "admin console", state: describe(portalFixture(), tree)}.body()
			if !strings.Contains(body, tc.want) {
				t.Errorf("the diagnostic does not name %q:\n%s", tc.want, body)
			}
		})
	}

	// The other half, or every case above is satisfied by a predicate that refuses
	// everything: a complete tree is built, and says nothing is missing.
	whole := adminFixture()
	if !whole.built() {
		t.Fatalf("a complete console tree reports unbuilt, missing %v", whole.missing)
	}
}

// TestNoExportedSymbolAdvertisesAUIThisBinaryCannotServe is the mirror of the
// release gate, and the **only** assertion in this file about the exported surface in
// the state a caller has to survive.
//
// `TestAnUnbuiltBinarySaysSoLoudly` constructs the handler directly, so it runs
// everywhere but proves nothing about what `Portal()` and `Admin()` return. This one
// can only run when `Built()` is false — a fresh clone, or a broken `COPY`.
//
// ── one name, two states ──────────────────────────────────────────────────────
//
// Its skip is `Built()`, and `Built()` is false in **two** different worlds:
// nothing embedded, and **half copied**. An image built from a broken `COPY` —
// portal intact, `admin-dist/assets/` missing — lands in the second, and a message
// saying *"in a binary with no documents"* would be a true failure carrying a
// false explanation.
//
// So it names the world it is actually in. That is the same discipline
// `docs/rules/database.md` asks for one file over — assert that the refusal names
// the object you meant — applied to a test's own account of itself.
func TestNoExportedSymbolAdvertisesAUIThisBinaryCannotServe(t *testing.T) {
	if Built() {
		t.Skip("a frontend is embedded, so the unserviceable path is unreachable here " +
			"(TestTheEmbeddedFrontendIsReal is the assertion that is live instead)")
	}

	// Which of the two worlds this is. Reported rather than assumed, because the
	// remedy differs: an empty tree is a build that did not run, a half-copied one is
	// a build that ran and lost half its output.
	// The same describer the 503 body and the release gate use. Three hand-written
	// accounts of this state is how one of them came to be wrong.
	state := describe(thePortal, theAdmin)
	t.Logf("running against a binary where %s", state)

	for name, h := range map[string]http.Handler{"Portal()": Portal(), "Admin()": Admin()} {
		for _, p := range []string{"/", "/en/", "/en/voucher/", "/sessions", "/assets/x.js"} {
			resp := get(t, h, p)
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("%s %s: status %d, want 503 (%s) — a binary that cannot serve a UI "+
					"must not answer like one that can", name, p, resp.StatusCode, state)
			}
			if resp.Header.Get("X-Foyer-UI") != "absent" {
				t.Errorf("%s %s: no X-Foyer-UI: absent (%s)", name, p, state)
			}
		}
	}

	// ── the composition hazard, asserted rather than only documented ─────────
	//
	// With no serviceable frontend, PortalHas is false everywhere. A caller reading
	// that as "route this to EntryHandler" rebuilds the loop, which is why Built() is
	// the gate.
	for _, p := range []string{"/", "/en/", "/vi/", "/en/voucher/"} {
		if PortalHas(p) {
			t.Errorf("PortalHas(%q) is true (%s)", p, state)
		}
	}
	// And the one that was actually wrong. In the half-copied world the portal really
	// does carry documents, so this is not "there are no languages" — it is "there are
	// none this binary will serve", which is a different sentence and the true one.
	if got := PortalLanguages(); len(got) != 0 {
		t.Errorf("PortalLanguages() is %v, but every path answers 503 (%s) — a caller "+
			"intersecting its site's advertised languages with this would negotiate "+
			"against documents that do not resolve", got, state)
	}
}

// TestNoExportAdvertisesWhatAHalfCopiedBinaryCannotServe drives the half-copied state
// on every run, in every world.
//
// The test above can only see this when the binary it is compiled into happens to be
// broken — which takes building a real image. This one reaches the same state
// directly, by passing built-ness and the tree independently: **a fully populated
// portal tree that `Built()` says is not serviceable**, which is precisely a
// console-broken image.
//
// It cannot be reproduced by emptying the portal's own directory: `languagesOf` on
// an empty tree is empty, so a missing guard has no visible effect in that world.
func TestNoExportAdvertisesWhatAHalfCopiedBinaryCannotServe(t *testing.T) {
	full := portalFixture()
	if got := portalLanguages(true, full); len(got) != 2 {
		t.Fatalf("a serviceable binary reports %v; the fixture carries two languages, so "+
			"this test would be vacuous", got)
	}
	if got := portalLanguages(false, full); len(got) != 0 {
		t.Fatalf("portalLanguages reports %v for a binary whose every path answers 503 — "+
			"the portal tree is populated and the console is not, which is a broken COPY, "+
			"and a caller would negotiate against languages it cannot serve", got)
	}
}

// TestAHalfCopiedTreeIsNotBuilt — one frontend is not a working deployment, and
// `Built()` must not report it as one.
func TestAHalfCopiedTreeIsNotBuilt(t *testing.T) {
	portalOnly := newPortalTree(fstest.MapFS{"en/index.html": {Data: []byte(realDocument("en", "x"))}})
	adminOnly := adminFixture()
	emptyPortal := newPortalTree(emptyFS{})
	emptyAdmin := newAdminTree(emptyFS{})

	if portalOnly.built() && emptyAdmin.built() {
		t.Fatal("unreachable")
	}
	if !portalOnly.built() || !adminOnly.built() {
		t.Fatal("a populated tree reports unbuilt: the predicate is inverted")
	}
	if emptyPortal.built() || emptyAdmin.built() {
		t.Fatal("an empty tree reports built")
	}
}

// TestTheEmbeddedFrontendIsReal is the release gate.
//
// It **skips** on a workstation that has not run `pnpm build`, because failing
// `go test` for that is a cost paid daily by everyone to describe a state that only
// matters at release. It **fails** when `FOYER_REQUIRE_WEBUI` is set, which is what
// an image build and `make ci` set — so a UI-less image cannot be produced quietly.
//
// The skip's reason is a condition a test can evaluate and that expires by itself,
// which is what `docs/rules/testing.md` requires of one. A phase number cannot.
func TestTheEmbeddedFrontendIsReal(t *testing.T) {
	if Built() {
		t.Log(describe(thePortal, theAdmin))
		return
	}
	// ── this sentence is the one an operator reads, so it is derived ─────────
	//
	// A literal such as *"internal/webui/portal-dist and internal/webui/admin-dist
	// hold no documents. Run `make web-embed`."* is wrong in every clause against
	// an image built from a broken `COPY` — portal intact, console assets missing:
	// both directories named as empty when one holds ten documents, and a remedy
	// sending the reader to re-run a frontend build that works, when the fault is
	// a `COPY` in a Dockerfile.
	//
	// It matters more here than in any other test in this file, because this is the
	// **only** assertion that fires in a half-copied binary — so it is what lands in
	// the deploy build's output, in `make check-all`'s, and in front of whoever is
	// working out why the deploy just failed. A diagnosis-shaped sentence pointing at
	// the wrong half of the system is worse than no sentence.
	msg := "this binary cannot serve a frontend. " + describe(thePortal, theAdmin) +
		"\n" + remedy(thePortal, theAdmin)
	if os.Getenv("FOYER_REQUIRE_WEBUI") != "" {
		t.Fatal(msg + "\nFOYER_REQUIRE_WEBUI is set, so this is a failure and not a skip.")
	}
	t.Skip(msg + "\nSet FOYER_REQUIRE_WEBUI=1 to make this a failure.")
}

// TestTheDiagnosisNamesTheStateItIsIn drives the describer through every world,
// unconditionally.
//
// It exists because the sentence it checks is the product's only output in a
// half-copied binary. One describer, and this is what says it stays honest:
// **each case asserts what must be said and what must not.**
//
// The negative halves are the point. "portal-dist holds no documents" appearing in a
// binary that holds ten is precisely a true failure with a false explanation, and
// an assertion that only checks for the right words cannot see it.
func TestTheDiagnosisNamesTheStateItIsIn(t *testing.T) {
	full, whole := portalFixture(), adminFixture()
	emptyP, emptyA := newPortalTree(emptyFS{}), newAdminTree(emptyFS{})
	brokenA := newAdminTree(fstest.MapFS{"index.html": {Data: []byte(
		`<!doctype html><html lang="en"><head>` +
			`<script type="module" src="/assets/index-AAAA1111.js"></script>` +
			`</head><body><div id="root"></div></body></html>`)}})
	landingless := newPortalTree(fstest.MapFS{
		"en/voucher/index.html": {Data: []byte(realDocument("en", "Voucher"))}})

	for _, tc := range []struct {
		name       string
		p          *portalTree
		a          *adminTree
		want, deny []string
		reBuild    bool // the remedy should say "the build did not run"
	}{
		{
			name: "nothing embedded at all",
			p:    emptyP, a: emptyA,
			want:    []string{"portal-dist holds no documents", "admin-dist holds no console document"},
			reBuild: true,
		},
		{
			// The world that shipped the wrong sentence.
			name: "half copied — the console lost its assets",
			p:    full, a: brokenA,
			want: []string{"portal-dist holds 5 documents", "2 languages (en, vi)",
				"missing the files it loads", "/assets/index-AAAA1111.js"},
			deny: []string{"portal-dist holds no documents", "admin-dist holds no console document"},
		},
		{
			name: "half copied the other way — the portal never landed",
			p:    emptyP, a: whole,
			want: []string{"portal-dist holds no documents", "admin-dist is complete"},
			deny: []string{"admin-dist holds no console document"},
		},
		{
			name: "sub-pages with no language landing",
			p:    landingless, a: whole,
			want: []string{"no language landing", "entry redirect has nowhere to go"},
			deny: []string{"portal-dist holds no documents"},
		},
		{
			name: "everything present",
			p:    full, a: whole,
			want: []string{"2 languages (en, vi)", "admin-dist is complete"},
			deny: []string{"no documents", "missing"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := describe(tc.p, tc.a)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("the diagnosis does not say %q:\n  %s", w, got)
				}
			}
			for _, d := range tc.deny {
				if strings.Contains(got, d) {
					t.Errorf("the diagnosis says %q, which is false of this binary:\n  %s", d, got)
				}
			}
			// The remedy has to point at the right half of the system. Telling somebody
			// to re-run a build that already ran correctly spends their attention on
			// the part that works.
			r := remedy(tc.p, tc.a)
			if tc.reBuild && !strings.Contains(r, "did not run") {
				t.Errorf("an empty tree should be diagnosed as a build that did not run:\n  %s", r)
			}
			if !tc.reBuild && !strings.Contains(r, "partially\ncopied") {
				t.Errorf("a populated tree should be diagnosed as a copy problem, not a missing build:\n  %s", r)
			}
		})
	}
}

// TestEveryEmbeddedExtensionHasAMediaType drives the real tree against the table in
// `mediaType`, so an asset kind the frontends have not shipped yet turns this red
// rather than arriving as `application/octet-stream`.
func TestEveryEmbeddedExtensionHasAMediaType(t *testing.T) {
	if !Built() {
		t.Skip("no frontend is embedded in this binary (run `make web-embed`)")
	}
	seen := map[string]bool{}
	for _, fsys := range []fs.FS{sub(portalDist, "portal-dist"), sub(adminDist, "admin-dist")} {
		_ = fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // walking for a census, not for content
			}
			if _, ok := urlPathFor(name); !ok {
				return nil
			}
			ext := strings.ToLower(path.Ext(name))
			seen[ext] = true
			if mediaType(name) == "application/octet-stream" {
				t.Errorf("%s: extension %q is not in the media-type table", name, ext)
			}
			return nil
		})
	}
	if len(seen) == 0 {
		t.Fatal("no files were censused; the walk is not reaching the trees")
	}
	// The other half: the table must actually be able to answer octet-stream, or
	// the loop above is satisfied by a function that returns a type for everything.
	if mediaType("thing.xyzzy") != "application/octet-stream" {
		t.Fatal("mediaType answers a real type for an unknown extension, so the census cannot fail")
	}
	t.Logf("extensions embedded: %v", sortedKeys(seen))
}

// ── helpers ──────────────────────────────────────────────────────────────────

func sortedRoutes(r routes) []string {
	out := make([]string, 0, len(r))
	for k := range r {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
