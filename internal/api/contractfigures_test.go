package api_test

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// Figures this package must not keep its own copy of, held to the contract.
//
// `docs/rules/database.md` states the general rule — **derive the
// stand-in from what it stands in for** — and the venue photograph's byte ceiling
// is the instance where two copies drift: 200 kB on one server against 100 kB in
// `web/contract/src/index.ts`, both citing the same measurement, with nothing
// comparing them. A console that accepts a photograph and a CI gate that rejects
// it produces an operator who has done nothing wrong and a build that is red.

// TestHeroImageCeilingIsTheContractsAndIsNotCopiedHere reads the figure a second
// way and requires the two to agree.
//
// **Two instruments, deliberately.** The server reads `x-max-decoded-bytes` out of
// the *embedded* document — base64, flate-compressed, re-serialised by the
// generator. This test reads the *source* `web/contract/openapi.yaml` as text. A
// single instrument here would be this package agreeing with itself: if codegen
// ever dropped extensions on the way through, the server's reader and a reader
// built the same way would both see nothing and both be satisfied.
//
// Scanned rather than parsed because the module carries no YAML parser and a
// dependency for one test is not worth it. The scan is anchored on the property name so that it cannot
// pick up some other schema's extension.
func TestHeroImageCeilingIsTheContractsAndIsNotCopiedHere(t *testing.T) {
	fromSource := heroCeilingInSource(t)
	if fromSource <= 0 {
		t.Fatalf("web/contract/openapi.yaml declares no x-max-decoded-bytes under "+
			"HeroImageUpload.dataBase64; nothing is the authority for the venue image "+
			"ceiling and every implementation is free to pick its own (read %d)", fromSource)
	}

	fromEmbedded := heroCeilingInEmbeddedSpec(t)
	if fromEmbedded != fromSource {
		t.Fatalf("the embedded specification says %d and the source document says %d — "+
			"regenerate, or find out what codegen did to the extension",
			fromEmbedded, fromSource)
	}

	// And the server actually refuses at that figure rather than at one of its own.
	// Asserted through the boundary rather than by reading the constant, because a
	// constant compared with itself is the shape this whole file is against.
	r := newRig(t)
	r.signIn(adminUser)

	atLimit := r.do(http.MethodPut, "/sites/"+testSite+"/portal/hero-image", heroBody(fromSource))
	atLimit.Body.Close()
	if atLimit.StatusCode != http.StatusOK {
		t.Errorf("exactly %d decoded bytes: got %d, want 200 — the ceiling is inclusive",
			fromSource, atLimit.StatusCode)
	}

	over := r.do(http.MethodPut, "/sites/"+testSite+"/portal/hero-image", heroBody(fromSource+1))
	body := readAll(t, over)
	over.Body.Close()
	if over.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("%d decoded bytes: got %d, want 413\n%s", fromSource+1, over.StatusCode, body)
	}

	// The refusal has to carry the figure, or the console renders "0 against a 0
	// limit" — the `below-minimum` mistake in a different field.
	var problem struct {
		Params struct {
			FieldParams map[string]map[string]any `json:"fieldParams"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		t.Fatalf("refusal is not a problem document: %v\n%s", err, body)
	}
	limit, ok := problem.Params.FieldParams["dataBase64"]["limitBytes"].(float64)
	if !ok {
		t.Fatalf("the refusal names no limitBytes for dataBase64\n%s", body)
	}
	if int(limit) != fromSource {
		t.Errorf("the refusal reports a limit of %d against a contract of %d", int(limit), fromSource)
	}
}

// TestLogoCeilingIsTheContractsAndIsNotCopiedHere is the same treatment for the
// figure that had no ceiling at all.
//
// **The hero image's risk is two servers disagreeing; the logo's is worse.**
// A `LOGO_MAX_BYTES` that lives only in `web/contract/src/index.ts` binds a
// console and binds no server, so the number the console sizes an upload against
// is applied by nothing downstream: `PATCH /sites/{id}/portal` with a **40 960
// byte** `logoDataUri` would be answered `200 OK` and stored. A logo lands inside
// the document, so that is five times the portal's entire 50 kB budget, paid on
// every launch because Android clears the cache each time, and
// `web/tools/check-budget.mjs` hard-fails on it — after the operator has already
// been told the logo was fine.
func TestLogoCeilingIsTheContractsAndIsNotCopiedHere(t *testing.T) {
	fromSource := figureInSource(t, "PortalSettingsUpdate", "logoDataUri", "x-max-decoded-bytes")
	if fromSource <= 0 {
		t.Fatalf("web/contract/openapi.yaml declares no x-max-decoded-bytes under "+
			"PortalSettingsUpdate.logoDataUri; nothing is the authority for the logo "+
			"ceiling and the server is free to pick its own, which is how it came to "+
			"have none (read %d)", fromSource)
	}
	fromEmbedded := figureInEmbeddedSpec(t, "x-max-decoded-bytes", "PortalSettingsUpdate", "logoDataUri")
	if fromEmbedded != fromSource {
		t.Fatalf("the embedded specification says %d and the source document says %d — "+
			"regenerate, or find out what codegen did to the extension",
			fromEmbedded, fromSource)
	}

	r := newRig(t)
	r.signIn(adminUser)

	// Exactly at the ceiling is accepted, one byte over is refused. Both halves,
	// because a server that refuses everything satisfies the second alone.
	atLimit := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{
		"logoDataUri": logoDataURI(fromSource),
	})
	atLimitBody := readAll(t, atLimit)
	atLimit.Body.Close()
	if atLimit.StatusCode != http.StatusOK {
		t.Errorf("exactly %d decoded bytes: got %d, want 200 — the ceiling is inclusive\n%s",
			fromSource, atLimit.StatusCode, atLimitBody)
	}

	over := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{
		"logoDataUri": logoDataURI(fromSource + 1),
	})
	body := readAll(t, over)
	over.Body.Close()
	if over.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("%d decoded bytes: got %d, want 413\n%s", fromSource+1, over.StatusCode, body)
	}

	var problem struct {
		Type   string `json:"type"`
		Params struct {
			FieldParams map[string]map[string]any `json:"fieldParams"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		t.Fatalf("refusal is not a problem document: %v\n%s", err, body)
	}
	if problem.Type != string(gen.ProblemstooLarge) {
		t.Errorf("refusal type is %q, want %q", problem.Type, gen.ProblemstooLarge)
	}
	limit, ok := problem.Params.FieldParams["logoDataUri"]["limitBytes"].(float64)
	if !ok {
		t.Fatalf("the refusal names no limitBytes for logoDataUri\n%s", body)
	}
	if int(limit) != fromSource {
		t.Errorf("the refusal reports a limit of %d against a contract of %d", int(limit), fromSource)
	}
	sent, ok := problem.Params.FieldParams["logoDataUri"]["bytes"].(float64)
	if !ok || int(sent) != fromSource+1 {
		t.Errorf("the refusal reports %v bytes for a %d byte logo — the console renders "+
			"both figures, and a wrong one is worse than a missing one", sent, fromSource+1)
	}

	// And the refusal is a refusal: the oversized logo is not what a subsequent
	// read returns. A ceiling that answers 413 and stores anyway is the same
	// defect wearing a status code.
	//
	// Measured by *size*, not by prefix. Two all-zero payloads differing by one
	// byte share every leading character, so a `Contains` here reports the
	// accepted logo as the refused one and is red against a correct server.
	get := r.do(http.MethodGet, "/sites/"+testSite+"/portal", nil)
	after := readAll(t, get)
	get.Body.Close()
	var stored gen.PortalSettings
	if err := json.Unmarshal([]byte(after), &stored); err != nil {
		t.Fatalf("decode PortalSettings: %v\n%s", err, after)
	}
	if stored.LogoDataUri == nil {
		t.Fatal("the accepted logo is gone, so this cannot tell a refusal from a wipe")
	}
	if n := decodedDataURIBytes(t, *stored.LogoDataUri); n > fromSource {
		t.Errorf("the stored logo decodes to %d bytes, over the %d byte ceiling — "+
			"the refusal was answered and then ignored", n, fromSource)
	}
}

// decodedDataURIBytes is the size of what a `data:` URI actually carries.
func decodedDataURIBytes(t *testing.T, uri string) int {
	t.Helper()
	_, payload, ok := strings.Cut(uri, ",")
	if !ok {
		t.Fatalf("not a data URI: %.40q", uri)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("data URI payload is not base64: %v", err)
	}
	return len(raw)
}

// TestPortalSettingsReportsTheBudgetsItEnforces holds the reported figures to the
// enforced ones **through the boundary**.
//
// This is the check that makes "reported" and "enforced" one number rather than
// two that happen to agree today. `budgets` was never emitted at all, so the
// console fell back to three literals of its own — 8192, 102400 and 51200 in
// `Portal.tsx` — and a console sizing an upload against a limit the server does not
// apply is precisely the failure the hero image already had once.
//
// It drives an upload at each reported figure plus one and requires the server to
// refuse it. Nothing here reads a constant: a second copy appearing on either side
// makes this red from whichever side moved.
func TestPortalSettingsReportsTheBudgetsItEnforces(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	get := r.do(http.MethodGet, "/sites/"+testSite+"/portal", nil)
	body := readAll(t, get)
	get.Body.Close()

	var settings gen.PortalSettings
	if err := json.Unmarshal([]byte(body), &settings); err != nil {
		t.Fatalf("decode PortalSettings: %v\n%s", err, body)
	}
	if settings.Budgets == nil {
		t.Fatal("PortalSettings carries no budgets, so the console has nothing to size " +
			"an upload against but numbers it typed in itself — all three members are " +
			"required, so this is the whole object missing rather than one figure")
	}
	// The portal's address is the process's, not the store's — and this rig wires
	// none, so null is the honest answer. The wired value is asserted where a
	// URL is actually configured, below: one direction each, because a preview
	// of somebody else's portal fails as quietly as a preview of nothing.
	if settings.PortalUrl != nil {
		t.Errorf("portalUrl = %q on a rig with no PortalURL wired, want null rather than a guess", *settings.PortalUrl)
	}
	if settings.Budgets.CodeBudgetBytes <= 0 || settings.Budgets.ImageMaxBytes <= 0 ||
		settings.Budgets.LogoMaxBytes <= 0 {
		t.Fatalf("a budget of zero admits nothing and reads as a rendering bug: %+v", *settings.Budgets)
	}

	over := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{
		"logoDataUri": logoDataURI(settings.Budgets.LogoMaxBytes + 1),
	})
	over.Body.Close()
	if over.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a logo one byte over the reported logoMaxBytes (%d) got %d, want 413 — "+
			"the figure the console shows is not the figure the server applies",
			settings.Budgets.LogoMaxBytes, over.StatusCode)
	}

	bigImage := r.do(http.MethodPut, "/sites/"+testSite+"/portal/hero-image",
		heroBody(settings.Budgets.ImageMaxBytes+1))
	bigImage.Body.Close()
	if bigImage.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("an image one byte over the reported imageMaxBytes (%d) got %d, want 413",
			settings.Budgets.ImageMaxBytes, bigImage.StatusCode)
	}

	// The code budget bounds no request, so it cannot be held to a refusal. It is
	// held to the document instead — the only other place it exists.
	declared := figureInSource(t, "PortalSettings", "codeBudgetBytes", "x-budget-bytes")
	if declared <= 0 {
		t.Fatal("web/contract/openapi.yaml declares no x-budget-bytes under " +
			"PortalSettings.budgets.codeBudgetBytes; the one figure with no field to " +
			"sit beside then has no home at all and every implementation invents it")
	}
	if settings.Budgets.CodeBudgetBytes != declared {
		t.Errorf("the server reports a code budget of %d against a contract of %d",
			settings.Budgets.CodeBudgetBytes, declared)
	}
}

// TestUpdatePortalSettingsAnswersWithTheBudgetsToo — the console's PATCH response
// is what it re-renders from, so a `budgets` present on the read and absent on the
// write is a form that loses its own limits the moment it is saved.
func TestUpdatePortalSettingsAnswersWithTheBudgetsToo(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	resp := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{"venueName": "The Other Venue"})
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH: %d\n%s", resp.StatusCode, body)
	}
	var settings gen.PortalSettings
	if err := json.Unmarshal([]byte(body), &settings); err != nil {
		t.Fatalf("decode PortalSettings: %v\n%s", err, body)
	}
	if settings.Budgets == nil {
		t.Fatal("updatePortalSettings answered without budgets")
	}
}

// TestPortalSettingsAnswersThePortalAddressItIsServedFrom — the console's
// preview needs an origin, and only the process serving the portal knows one.
// A rig wiring `PortalURL: http://portal.example/en/` answers the origin
// without the trailing slash; a rig wiring nothing answers null rather than a
// guess. The negative half lives on the two reads above.
func TestPortalSettingsAnswersThePortalAddressItIsServedFrom(t *testing.T) {
	store := world(t)
	srv, err := api.New(api.Config{
		Ports:     store.Ports(),
		PortalURL: "http://portal.example/en/",
		Now:       func() time.Time { return at(0) },
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	r := &rig{t: t, server: ts, store: store}
	r.signIn(adminUser)

	resp := r.do(http.MethodGet, "/sites/"+testSite+"/portal", nil)
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d\n%s", resp.StatusCode, body)
	}
	var settings gen.PortalSettings
	if err := json.Unmarshal([]byte(body), &settings); err != nil {
		t.Fatalf("decode PortalSettings: %v\n%s", err, body)
	}
	if settings.PortalUrl == nil || *settings.PortalUrl != "http://portal.example/en" {
		t.Errorf("portalUrl answers %v, want the configured origin without its trailing slash", settings.PortalUrl)
	}

	patched := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{"venueName": "The Other Venue"})
	patchedBody := readAll(t, patched)
	patched.Body.Close()
	if patched.StatusCode != http.StatusOK {
		t.Fatalf("PATCH: %d\n%s", patched.StatusCode, patchedBody)
	}
	var after gen.PortalSettings
	if err := json.Unmarshal([]byte(patchedBody), &after); err != nil {
		t.Fatalf("decode PortalSettings: %v\n%s", err, patchedBody)
	}
	if after.PortalUrl == nil || *after.PortalUrl != "http://portal.example/en" {
		t.Errorf("the write answers portalUrl %v, want the same origin the read answers", after.PortalUrl)
	}
}

// logoDataURI is a base64 `data:` URI whose *decoded* payload is `decoded` bytes.
func logoDataURI(decoded int) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, decoded))
}

func heroBody(decoded int) map[string]any {
	return map[string]any{
		"filename":    "venue.jpg",
		"contentType": string(gen.Imagejpeg),
		"dataBase64":  base64.StdEncoding.EncodeToString(make([]byte, decoded)),
	}
}

// TestVenueAssetServesTheStoredPhotograph — the bytes behind `HeroImage.url`.
// PUT stores; the public handler serves the same bytes with the stored content
// type; DELETE removes them. Unknown sites and bad paths are 404, never a
// document and never a guess. Driven here rather than in `cmd/foyer` because
// the bytes live behind the port, and the listener test only pins the mount.
func TestVenueAssetServesTheStoredPhotograph(t *testing.T) {
	store := world(t)
	srv, err := api.New(api.Config{Ports: store.Ports(), Now: func() time.Time { return at(0) }})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	r := &rig{t: t, store: store}
	// The rig signs in through the console surface; the bytes are served on a
	// separate handler that `cmd/foyer` mounts beside `/api/v0`. Two servers,
	// one store — the same split production has.
	auth := httptest.NewServer(srv.Handler())
	t.Cleanup(auth.Close)
	r.server = auth
	r.signIn(adminUser)

	ts := httptest.NewServer(srv.VenueAssetHandler())
	t.Cleanup(ts.Close)

	payload := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46}
	put := r.do(http.MethodPut, "/sites/"+testSite+"/portal/hero-image", map[string]any{
		"filename": "venue.jpg", "contentType": string(gen.Imagejpeg),
		"dataBase64": base64.StdEncoding.EncodeToString(payload),
	})
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT hero: %d", put.StatusCode)
	}

	get, err := ts.Client().Get(ts.URL + "/venue/" + testSite + "/venue.jpg")
	if err != nil {
		t.Fatalf("GET venue asset: %v", err)
	}
	body, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET venue asset: %d", get.StatusCode)
	}
	if ct := get.Header.Get("Content-Type"); ct != string(gen.Imagejpeg) {
		t.Errorf("Content-Type = %q, want the stored type rather than a sniffed one", ct)
	}
	if !bytes.Equal(body, payload) {
		t.Errorf("served %d bytes, want the %d stored", len(body), len(payload))
	}
	if cc := get.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: re-upload replaces the row at the same URL", cc)
	}

	head, err := ts.Client().Head(ts.URL + "/venue/" + testSite + "/renamed.jpg")
	if err != nil {
		t.Fatalf("HEAD venue asset: %v", err)
	}
	head.Body.Close()
	if head.StatusCode != http.StatusOK {
		t.Errorf("HEAD with a renamed file: %d, want 200 — the row is resolved by site, not filename", head.StatusCode)
	}

	missing, err := ts.Client().Get(ts.URL + "/venue/site-nowhere/hero.jpg")
	if err != nil {
		t.Fatalf("GET unknown site: %v", err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown site: %d, want 404 rather than a guess", missing.StatusCode)
	}

	del := r.do(http.MethodDelete, "/sites/"+testSite+"/portal/hero-image", nil)
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE hero: %d", del.StatusCode)
	}
	gone, err := ts.Client().Get(ts.URL + "/venue/" + testSite + "/venue.jpg")
	if err != nil {
		t.Fatalf("GET after delete: %v", err)
	}
	gone.Body.Close()
	if gone.StatusCode != http.StatusNotFound {
		t.Errorf("GET after delete: %d, want 404", gone.StatusCode)
	}
}

func heroCeilingInSource(t *testing.T) int {
	t.Helper()
	return figureInSource(t, "HeroImageUpload", "dataBase64", "x-max-decoded-bytes")
}

// figureInSource scans the contract document as text for one declared figure.
//
// Scanned rather than parsed because the module carries no YAML parser and a
// dependency for one test is not worth it. Anchored on the schema *and* the property so that it cannot
// pick up another schema's extension — `logoDataUri` appears in three schemas and
// only one of them declares a ceiling.
func figureInSource(t *testing.T, schema, property, extension string) int {
	t.Helper()
	f, err := os.Open("../../web/contract/openapi.yaml")
	if err != nil {
		t.Fatalf("open the contract: %v", err)
	}
	defer f.Close()

	inSchema, fieldIndent := false, -1
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scan.Scan() {
		line := scan.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "    "+schema+":"):
			inSchema = true
		case inSchema && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") &&
			strings.HasSuffix(trimmed, ":"):
			// The next schema at the same indentation; the block is over.
			inSchema, fieldIndent = false, -1
		case inSchema && trimmed == property+":":
			fieldIndent = len(line) - len(strings.TrimLeft(line, " "))
		case fieldIndent >= 0 && strings.HasPrefix(trimmed, extension+":"):
			_, value, _ := strings.Cut(line, ":")
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				t.Fatalf("%s is not a number: %q", extension, value)
			}
			return n
		case fieldIndent >= 0 && trimmed != "" && !strings.HasPrefix(trimmed, "#") &&
			len(line)-len(strings.TrimLeft(line, " ")) <= fieldIndent:
			// Out of the property's block without having seen the extension.
			// Leaving this open is how a scan finds the *next* property's figure
			// and reports it as this one's — green, and about the wrong field.
			fieldIndent = -1
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatalf("read the contract: %v", err)
	}
	return 0
}

func heroCeilingInEmbeddedSpec(t *testing.T) int {
	t.Helper()
	return figureInEmbeddedSpec(t, "x-max-decoded-bytes", "HeroImageUpload", "dataBase64")
}

// figureInEmbeddedSpec reads the same figure the server reads, by the path the
// server walks.
func figureInEmbeddedSpec(t *testing.T, extension string, path ...string) int {
	t.Helper()
	raw, err := gen.GetSpecJSON()
	if err != nil {
		t.Fatalf("embedded spec: %v", err)
	}
	// Decoded generically rather than into a shaped struct: this test's job is to
	// be a *second instrument*, and a struct carrying the server's own json tags
	// would be the server's reader written twice and satisfied by its own mistake.
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("embedded spec is not JSON: %v", err)
	}
	cur, _ := doc["components"].(map[string]any)
	cur, _ = cur["schemas"].(map[string]any)
	for i, hop := range path {
		next, ok := cur[hop].(map[string]any)
		if !ok {
			t.Fatalf("the embedded specification has no %s", strings.Join(path[:i+1], "."))
		}
		cur = next
		if i < len(path)-1 {
			props, ok := cur["properties"].(map[string]any)
			if !ok {
				t.Fatalf("%s declares no properties", strings.Join(path[:i+1], "."))
			}
			cur = props
		}
	}
	n, ok := cur[extension].(float64)
	if !ok {
		return 0
	}
	return int(n)
}
