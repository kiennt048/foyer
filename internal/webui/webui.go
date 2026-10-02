// Package webui serves the two frontends out of the Go binary that already
// answers their API calls.
//
// ── the defect this closes ───────────────────────────────────────────────────
//
// Without it, `http://<host>/` answers `302` to `/en/`, and `/en/` answers `302`
// to `/en/` — **an infinite redirect loop, 26 bytes, no HTML anywhere in it** —
// and the console on `:8082` answers `404`. A binary with no web assets serves no
// frontend at all.
//
// The cause is worth stating precisely, because the shape recurs. A portal mux
// with four routes — RFC 8908, `/api/v0/`, `/healthz`, and `/` as a catch-all —
// lets `/en/` fall through to `/`, and `/` is [portal.Service.EntryHandler], whose
// entire job is to redirect to `/en/`. **Both halves are correct and tested.**
// `cmd/foyer/listeners_test.go` and `internal/portal/entry_test.go` assert the
// `302` is right, and it is. What is needed is the test that asks *and then what*
// — the one that fetches the redirect's target and asserts a document comes back.
// That test is `TestTheEntryRedirectTargetIsAlwaysADocument` in this package.
//
// ── why the Go binary, and not nginx ─────────────────────────────────────────
//
// The Go binary is what serves this in production, because a static file server
// **cannot see a request**. It can therefore neither read `Accept-Language` (the
// portal negotiates it against the *site's advertised* languages, which live in
// the database) nor relay the query string carrying the
// `ap`, `id`, `t`, `ssid` and `url` that `VerifyPresence` is the only evidence for
// (`docs/rules/security.md` §4).
//
// Two further reasons, decided rather than inherited: every additional origin is
// another entry in the walled garden **on every driver**, and the design makes
// "a stranger installs it and a phone gets online" a *timed* release goal, so a
// second container is another way to lose those five minutes.
//
// ── what a caller has to know, and it is one sentence ────────────────────────
//
// **Check [Built] before composing anything.** When it is false this package has no
// documents, [PortalHas] answers false for every path, and both handlers answer
// `503` on every path — so a caller that consults [PortalHas] to decide between
// this package and the entry redirect, and falls back to the entry redirect on
// false, **reconstructs the redirect loop exactly.** Mount [Portal] as the
// catch-all instead, or refuse to start. [Built] is the gate; [PortalHas] is for
// deciding which of two document-serving paths owns a request, never for deciding
// whether documents exist.
package webui

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// The two embedded trees, populated from `web/portal/dist` and `web/admin/dist`
// by `make web-embed` and by the node stage in `deploy/Dockerfile.foyer`.
//
// ── `all:`, and the committed `.gitkeep` it exists for ───────────────────────
//
// `//go:embed` is a **compile-time** directive: a missing directory is a build
// error, not a runtime absence. Left to itself that would break `go build ./...`
// on every fresh clone and in every CI job that does not run `pnpm build` — which
// is a cost paid by everyone, forever, to describe a state that is only interesting
// at release. So each directory carries one committed file, `.gitkeep`, and
// nothing else is tracked in either.
//
// A bare `//go:embed portal-dist` refuses a directory whose only file is a dotfile
// ("contains no embeddable files"), which is what makes `all:` load-bearing rather
// than a precaution. It is also the right directive on its own merits: the portal's
// build is a custom script (`web/portal/build/build.ts`), not Vite, so a future
// dotfile would be **silently dropped** by the bare form — absent from the binary,
// with a green build.
//
// `all:` embeds dotfiles, so a workstation's stray `.DS_Store` lands in the tree
// too. It is never reachable: [urlPathFor] keeps it out of the route table and
// [hiddenSegment] refuses it again at request time.
//
//go:embed all:portal-dist
var portalDist embed.FS

//go:embed all:admin-dist
var adminDist embed.FS

// The trees this binary actually serves.
//
// They are package variables because the exported API takes no arguments, and they
// are the **only** two values in this file that are: every behaviour below is a
// method on a tree built from an arbitrary [io/fs.FS], so a test can construct the
// unbuilt world, the placeholder world and the half-copied world and drive the same
// code against them. That is not a convenience — it is what lets the assertions in
// `webui_test.go` re-inject the defects they guard on every run, instead of
// trusting that somebody once watched them fail.
var (
	thePortal = newPortalTree(sub(portalDist, "portal-dist"))
	theAdmin  = newAdminTree(sub(adminDist, "admin-dist"))
)

// Built reports whether this binary carries a real frontend.
//
// ── it is derived from the artefacts, never from the placeholder ─────────────
//
// The tempting implementation is "the `.gitkeep` is gone". That is a stand-in for
// the fact rather than the fact, and `docs/rules/testing.md` has a section on
// what those cost: the build step may copy over the top without deleting, and the
// image build excludes both directories from the build context entirely, so the
// placeholder is present in one world and absent in the other with no bearing on
// whether a document exists in either.
//
// So it asks the thing a caller actually needs: **is there a portal document to
// serve, and is there an admin document to serve.** A tree holding only a
// placeholder answers false; a half-copied tree answers false for the half that is
// missing; and neither can be fixed by touching a marker file.
//
// It is deliberately one boolean rather than two. A binary serving one of the two
// frontends is not a state anything downstream should be able to describe as
// working, and the diagnostic body names which tree is empty for whoever has to fix
// it.
func Built() bool { return thePortal.built() && theAdmin.built() }

// Portal returns the guest portal's documents.
//
// Exact-match only, and that is the whole routing story: eleven self-contained
// documents, no assets, no content hashes, nothing to fall back to. A path that is
// not a document is a `404` — which is what keeps this handler safe as a mux
// catch-all, since `/api/v0/…` and `/.well-known/captive-portal` cannot become
// documents by accident.
//
// **`/` is not among them.** See [newPortalTree].
func Portal() http.Handler {
	if !Built() {
		return absentHandler("portal")
	}
	return thePortal
}

// PortalHas reports whether [Portal] will answer urlPath with a document.
//
// It is for a caller deciding **which** document-serving path owns a request. It is
// not a test of whether documents exist — read [Built] for that, and read this
// package's doc comment for why confusing the two rebuilds the redirect loop.
func PortalHas(urlPath string) bool {
	if !Built() {
		return false
	}
	_, _, ok := thePortal.resolve(urlPath)
	return ok
}

// PortalCSS returns the guest portal's inlined stylesheet, extracted from the
// embedded artefact's own document. One copy of the bytes, not two: the
// per-request renderer wears the same CSS the static build shipped, and a
// stylesheet edit rebuilds both paths together. Empty where no artefact
// answers — the rendered body still serves, unstyled, rather than refusing.
func PortalCSS() string {
	if !Built() {
		return ""
	}
	a, _, ok := thePortal.resolve("/en/")
	if !ok || a == nil {
		return ""
	}
	return extractCSS(a.body)
}

// extractCSS reads the `<style>…</style>` the build inlined into a document.
// Index arithmetic, not a parser: the artefact is this repository's own build
// output, and a missing pair answers empty rather than half a stylesheet.
func extractCSS(body []byte) string {
	s := string(body)
	start := strings.Index(s, "<style>")
	if start < 0 {
		return ""
	}
	rest := s[start+len("<style>"):]
	end := strings.Index(rest, "</style>")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// PortalLanguages returns the languages this binary has documents for, sorted.
//
// ── it is here because two sets can disagree and nothing made them agree ─────
//
// `EntryHandler` negotiates `Accept-Language` against the **site's advertised**
// languages, which come from the database. The documents come from the frontend
// build. Nothing connects those two, so an operator who advertises a language the
// build does not carry sends every guest to a path with no document behind it —
// answered here as a `404`, which is at least visible, but still a venue nobody can
// get online at.
//
// The fix belongs at the composition point, not here: intersect the site's
// advertised languages with this before handing them to `EntryHandler`, and the
// negotiation cannot name a document that does not exist.
// `TestASiteAdvertisingALanguageThePortalCannotServeGets404AndNotALoop` holds the
// `404`; this is what lets a caller avoid needing it.
//
// ── it is gated on [Built] ───────────────────────────────────────────────────
//
// A half-copied image — `admin-dist/index.html` landed and `assets/` did not, as
// `deploy/Dockerfile.foyer` built against a deliberately broken copy produces — is
// a state no test in this package could reach on its own.
//
// In that binary every exported symbol reports "no UI": `Portal()` and `Admin()`
// answer `503` and `PortalHas` answers false. Ungated, `PortalLanguages()` would
// answer `[en vi]` for documents that would `503`, so a caller doing exactly what
// the paragraph above tells it to do — intersect the site's advertised languages
// with this — would negotiate against two languages the binary cannot serve. **That is the hazard this function exists to prevent,
// arriving through the door the function itself opened.**
//
// The guard is [Built] and not `thePortal.built()`, deliberately: a binary whose
// console is broken is not a working deployment, and one export answering as though
// it were is how a caller ends up trusting a gate that the rest of the package has
// already closed.
func PortalLanguages() []string { return portalLanguages(Built(), thePortal) }

// portalLanguages is the body, split out so the half-copied state is reachable from
// a test.
//
// The state that found the defect — a populated portal tree with `built` false —
// cannot be produced by emptying an embed directory, because emptying the portal's
// makes `languagesOf` empty too and the bug disappears. Passing the two independently
// is what lets `TestNoExportAdvertisesWhatAHalfCopiedBinaryCannotServe` drive it on
// every run rather than only inside a broken image.
func portalLanguages(built bool, t *portalTree) []string {
	if !built {
		return nil
	}
	return languagesOf(t)
}

// Admin returns the console.
//
// A single-page application, so an unknown path is the `index.html` fallback and a
// deep link survives a reload — with two carve-outs that are not decoration. See
// [adminTree.ServeHTTP].
func Admin() http.Handler {
	if !Built() {
		return absentHandler("admin console")
	}
	return theAdmin
}

// ── the embedded trees ───────────────────────────────────────────────────────

// asset is one embedded file, resolved once at startup.
//
// The body is read at construction rather than per request. That costs about
// 770 kB of heap for a tree already in the binary's read-only data, and buys the
// property below.
type asset struct {
	body         []byte
	contentType  string
	etag         string
	cacheControl string
}

// ── the traversal argument, which is structural rather than a check ──────────
//
// **No request path ever reaches a filesystem.** Both trees use the cleaned request
// path as a **map key** and nothing else; a miss is a `404`. There is no
// `Open(userInput)` anywhere in this package, so `..`, percent-encoded `..`,
// absolute paths, NUL bytes and backslashes are not defended against — they are
// unrepresentable as a key that the walk below did not put there.
//
// `TestNoPathEscapesTheEmbeddedTree` drives the table anyway, because a structural
// argument nobody re-checks is a comment.
type routes map[string]*asset

// sub narrows an embedded tree to its directory, or yields an empty one.
//
// An error here means the embed is malformed, which can only happen at build time.
// It becomes an empty tree rather than a panic, so that the answer is [Built]
// reporting false and a loud `503` — at runtime, where an operator can read it —
// instead of a process that will not start and takes the RADIUS data plane down
// with the console.
func sub(root embed.FS, prefix string) fs.FS {
	s, err := fs.Sub(root, prefix)
	if err != nil {
		return emptyFS{}
	}
	return s
}

type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

// load walks one tree and turns it into a route table.
//
// A tree that yields nothing is not an error. It is the unbuilt binary, and it is
// reported by [Built] and answered by [absentHandler].
func load(fsys fs.FS, cache func(urlPath string) string) routes {
	out := routes{}
	_ = fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is an absent one; Built() reports it
		}
		urlPath, ok := urlPathFor(name)
		if !ok {
			return nil
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil //nolint:nilerr // same: absence is the answer, never a panic
		}
		out[urlPath] = &asset{
			body:         body,
			contentType:  mediaType(name),
			etag:         etagOf(body),
			cacheControl: cache(urlPath),
		}
		return nil
	})
	return out
}

// urlPathFor maps an embedded file name onto the URL it is served at.
//
// `en/voucher/index.html` becomes `/en/voucher/` — trailing slash, because that is
// what the built documents link to (`href="/en/voucher/"`) and a portal whose own
// links 404 is not a portal. Everything else keeps its name.
//
// It is also the gate that keeps `all:`'s dotfiles out of the route table, so a
// `.gitkeep` or a `.DS_Store` is not merely refused at request time — it is never a
// route at all.
func urlPathFor(name string) (string, bool) {
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "_") {
			return "", false
		}
	}
	if path.Base(name) == "index.html" {
		dir := path.Dir(name)
		if dir == "." {
			return "/", true
		}
		return "/" + dir + "/", true
	}
	return "/" + name, true
}

// ── serving one asset ────────────────────────────────────────────────────────

func (a *asset) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", a.contentType)
	h.Set("Cache-Control", a.cacheControl)
	// Every response, including the octet-stream fallback for an extension the
	// media-type table has not met. Without it a future asset of an unknown kind is
	// sniffable into script by the user agent.
	h.Set("X-Content-Type-Options", "nosniff")
	if a.etag != "" {
		h.Set("ETag", a.etag)
	}
	// ServeContent handles HEAD, Range and the conditional request against the ETag
	// above, and leaves the Content-Type alone because it is already set. A zero
	// modtime is honest: an embedded file has no meaningful one, and inventing the
	// build time would make every rebuild look like a change to every document.
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(a.body))
}

// absentHandler is what a binary with no UI answers, on every path.
//
// ── why it is 503 and not a blank page ───────────────────────────────────────
//
// `internal/version` is the shape being followed. It treats *"this build cannot
// name itself"* as a real answer worth reporting — `version.Unknown`, which
// `deploy/verify.sh` reads as drift — rather than a default to paper over, because
// *"the silence is the failure mode"*. The same is true here and the stakes are
// higher: a UI-less binary answering `200` with an empty body looks to every
// automated check like a working portal, and to a guest like a broken phone.
//
// So: `503`, never a `200`. `X-Foyer-UI: absent` for anything reading headers.
// `no-store`, so a CNA or a proxy cannot pin the failure in front of a repaired
// deployment. And a body that names the build step, because the person reading it
// is the person who can fix it.
func absentHandler(which string) http.Handler {
	return absent{which: which, state: describe(thePortal, theAdmin), remedy: remedy(thePortal, theAdmin)}
}

type absent struct {
	which  string
	state  string
	remedy string
}

func (a absent) body() string {
	return "Foyer: this binary cannot serve the " + a.which + ".\n\n" +
		a.state + "\n\n" + a.remedy + "\n\n" +
		"This is a build defect, not a configuration one. No setting turns it on.\n"
}

// describe says what this binary actually carries.
//
// ── one describer ────────────────────────────────────────────────────────────
//
// With separate hand-written accounts of the same state — this function's, the
// release gate's, and the exported-surface test's — a real image built from a
// broken `COPY` can be described as *"portal-dist and admin-dist hold no
// documents"* while its `portal-dist` holds **ten**: a true failure with a false
// explanation, in the message the deploy build and `make check-all` actually
// print, sending a reader to re-run `make web-embed`, which works, when the fault
// is a `COPY` in a Dockerfile. Two copies agreeing and one not is the arithmetic
// that makes a second copy of a fact a defect rather than a duplication. There is
// one, and every surface reads it.
func describe(p *portalTree, a *adminTree) string {
	var portal string
	if langs := languagesOf(p); len(langs) > 0 {
		portal = fmt.Sprintf("portal-dist holds %d documents in %d languages (%s)",
			len(p.routes), len(langs), strings.Join(langs, ", "))
	} else if len(p.routes) > 0 {
		portal = fmt.Sprintf("portal-dist holds %d documents but no language landing "+
			"(no `en/index.html`-shaped file), so the entry redirect has nowhere to go",
			len(p.routes))
	} else {
		portal = "portal-dist holds no documents"
	}

	var admin string
	switch {
	case a.index == nil:
		admin = "admin-dist holds no console document"
	case len(a.missing) > 0:
		admin = fmt.Sprintf("admin-dist has index.html but is missing the files it loads: %s",
			strings.Join(a.missing, ", "))
	default:
		admin = fmt.Sprintf("admin-dist is complete (%d files)", len(a.routes))
	}
	return portal + "; " + admin + "."
}

// remedy names the thing that is actually wrong, which is not always the same thing.
//
// **An empty tree and a half-copied one have different causes and different fixes**,
// and telling somebody to re-run a build that already ran correctly is worse than
// saying nothing: it spends their attention on the half of the system that works.
func remedy(p *portalTree, a *adminTree) string {
	if len(p.routes) == 0 && a.index == nil {
		return "The frontend build did not run. `make web-embed` (or `pnpm -F @foyer/portal build\n" +
			"&& pnpm -F @foyer/admin build`, then sync each dist into internal/webui/)."
	}
	return "The frontend build ran and its output was lost on the way in — this is a partially\n" +
		"copied tree, not a missing build. Check the COPY/sync steps that populate\n" +
		"internal/webui/portal-dist and internal/webui/admin-dist, and note that a sync must\n" +
		"delete before it copies or a stale content-hashed asset survives beside the new one."
}

func (a absent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := a.body()
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Foyer-UI", "absent")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusServiceUnavailable)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
}

// ── path handling ────────────────────────────────────────────────────────────

// cleanPath returns a lexically safe path, preserving the trailing slash that
// distinguishes a portal document from its slash-less form.
//
// `path.Clean` on a rooted path resolves every `.` and `..` and cannot leave one
// behind, so what comes back is inside the tree by construction. It also strips the
// trailing slash, which matters here — `/en/` and `/en` are different routes —
// hence the restore.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	cleaned := path.Clean(p)
	if cleaned != "/" && strings.HasSuffix(p, "/") {
		cleaned += "/"
	}
	return cleaned
}

// hiddenSegment refuses a path with a segment beginning `.` or `_`.
//
// `all:` puts dotfiles in the embedded tree; [urlPathFor] already keeps them out of
// the route table, and this is the second gate — so a route table that one day
// gains such an entry some other way still cannot serve one.
func hiddenSegment(urlPath string) bool {
	for _, seg := range strings.Split(urlPath, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "_") {
			return true
		}
	}
	return false
}

func etagOf(body []byte) string {
	// FNV-1a over the body. Not a security property — an ETag only has to change
	// when the bytes change, and the bytes are compiled in, so a collision costs a
	// stale asset on a console rather than anything an attacker gets to choose.
	const offset64, prime64 = uint64(14695981039346656037), uint64(1099511628211)
	h := offset64
	for _, b := range body {
		h ^= uint64(b)
		h *= prime64
	}
	return `"` + strconv.FormatUint(h, 16) + `"`
}

// mediaType is an explicit table, and the omission of `mime.TypeByExtension` is the
// point.
//
// That function reads the machine's `/etc/mime.types` (and, on Windows, the
// registry), so the `Content-Type` on a built artefact would depend on the host that
// ran the build. A wrong media type is already one of the mechanisms in this
// repository that looked like a check and was not; a *host-dependent* one cannot
// even be reproduced.
//
// `TestEveryEmbeddedExtensionHasAMediaType` drives the real tree against this table,
// so an asset kind the frontends have not shipped yet turns a test red instead of
// arriving as `application/octet-stream`.
func mediaType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".webmanifest":
		return "application/manifest+json"
	default:
		return "application/octet-stream"
	}
}
