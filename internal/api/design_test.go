package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// The portal page design through the HTTP surface: the refusals beyond the schema,
// in a fixed order, on one field key; the design read back whole; and the guest
// reading only what is live today — from the context and from the rendered page.

type designProblem struct {
	Type   string            `json:"type"`
	Errors map[string]string `json:"errors"`
	Params struct {
		Fields map[string]string `json:"fields"`
	} `json:"params"`
}

func patchDesign(t *testing.T, r *rig, design map[string]any) (int, string) {
	t.Helper()
	resp := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{"design": design})
	body := readAll(t, resp)
	resp.Body.Close()
	return resp.StatusCode, body
}

func design(blocks ...map[string]any) map[string]any {
	if blocks == nil {
		blocks = []map[string]any{}
	}
	return map[string]any{"accent": nil, "headline": map[string]any{}, "blocks": blocks}
}

func block(id, kind, placement string, extra map[string]any) map[string]any {
	b := map[string]any{
		"id": id, "kind": kind, "placement": placement, "visible": true,
		"content": map[string]any{"en": map[string]any{"title": "T", "label": "L"}},
	}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func TestPortalDesignRefusalsAreTheSpecsInTheSpecsOrder(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	fb := []map[string]any{{"network": "facebook", "url": "https://facebook.com/cafe7"}}
	cases := []struct {
		name   string
		blocks []map[string]any
		fault  string
		block  string
	}{
		{"duplicate id", []map[string]any{
			block("a", "text", "welcome", nil), block("a", "text", "welcome", nil),
		}, "duplicate", "Block 2:"},
		{"button before sign-in", []map[string]any{
			block("a", "text", "welcome", nil), block("b", "button", "welcome", map[string]any{"url": "https://a.example"}),
		}, "link-before-sign-in", "Block 2:"},
		{"social before sign-in", []map[string]any{
			block("s", "social", "welcome", map[string]any{"links": fb}),
		}, "link-before-sign-in", "Block 1:"},
		{"button with a blank url", []map[string]any{
			block("b", "button", "connected", map[string]any{"url": "   "}),
		}, "required", "Block 1:"},
		{"button with no url", []map[string]any{
			block("b", "button", "connected", nil),
		}, "required", "Block 1:"},
		{"button to javascript:", []map[string]any{
			block("b", "button", "connected", map[string]any{"url": "javascript:alert(1)"}),
		}, "not-a-link", "Block 1:"},
		{"mailto is not a link", []map[string]any{
			block("b", "button", "connected", map[string]any{"url": "mailto:hi@cafe.example"}),
		}, "not-a-link", "Block 1:"},
		{"social link with userinfo", []map[string]any{
			block("s", "social", "connected", map[string]any{"links": []map[string]any{
				{"network": "website", "url": "https://user@evil.example"},
			}}),
		}, "not-a-link", "Block 1:"},
		{"ends before it starts", []map[string]any{
			block("o", "offer", "welcome", map[string]any{"startsOn": "2026-09-30", "endsOn": "2026-09-01"}),
		}, "ends-before-start", "Block 1:"},
		// First failure wins, in the spec's order rather than the blocks': the
		// duplicate is the later block and is still the one answered.
		{"order is by rule, not by block", []map[string]any{
			block("b", "button", "welcome", map[string]any{"url": "https://a.example"}),
			block("b", "text", "welcome", nil),
		}, "duplicate", "Block 2:"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := patchDesign(t, r, design(c.blocks...))
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("got %d, want 422\n%s", code, body)
			}
			var p designProblem
			if err := json.Unmarshal([]byte(body), &p); err != nil {
				t.Fatalf("not a problem document: %v\n%s", err, body)
			}
			if p.Type != string(gen.Problemsvalidation) {
				t.Errorf("type %q", p.Type)
			}
			if p.Params.Fields["design"] != c.fault {
				t.Errorf("fault %q, want %q\n%s", p.Params.Fields["design"], c.fault, body)
			}
			if !strings.HasPrefix(p.Errors["design"], c.block) {
				t.Errorf("prose %q does not name %s", p.Errors["design"], c.block)
			}
		})
	}

	// A refused design is not stored.
	get := r.do(http.MethodGet, "/sites/"+testSite+"/portal", nil)
	var stored gen.PortalSettings
	if err := json.Unmarshal([]byte(readAll(t, get)), &stored); err != nil {
		t.Fatal(err)
	}
	get.Body.Close()
	if len(stored.Design.Blocks) != 0 {
		t.Errorf("a refused design was stored: %+v", stored.Design.Blocks)
	}
}

// A hidden block is a draft: a template's "Review us" button starts hidden with
// no link in it, and refusing that would refuse the template. The rules that
// do not concern a link still apply to it.
func TestAHiddenBlockMayHoldAnUnfinishedLink(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	draft := block("b", "button", "connected", map[string]any{"url": "", "visible": false})
	if code, body := patchDesign(t, r, design(draft)); code != http.StatusOK {
		t.Fatalf("hidden button with no link: %d, want 200\n%s", code, body)
	}
	bad := block("s", "social", "connected", map[string]any{"visible": false,
		"links": []map[string]any{{"network": "website", "url": "not a link"}}})
	if code, body := patchDesign(t, r, design(bad)); code != http.StatusOK {
		t.Fatalf("hidden social block with an unfinished link: %d, want 200\n%s", code, body)
	}
	early := block("s", "social", "welcome", map[string]any{"visible": false})
	if code, _ := patchDesign(t, r, design(early)); code != http.StatusUnprocessableEntity {
		t.Errorf("a hidden link block before sign-in: %d, want 422 — placement is not a draft's to relax", code)
	}
}

func TestPortalDesignSchemaIsTheRequestValidators(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	for _, accent := range []string{"#FFAA00", "#fff", "red", "#c8873a;x"} {
		d := design()
		d["accent"] = accent
		code, body := patchDesign(t, r, d)
		if code != http.StatusUnprocessableEntity {
			t.Errorf("accent %q: %d, want 422\n%s", accent, code, body)
			continue
		}
		var p designProblem
		_ = json.Unmarshal([]byte(body), &p)
		if _, ok := p.Params.Fields["design.accent"]; !ok {
			t.Errorf("accent %q refused without naming design.accent: %s", accent, body)
		}
	}
	long := block("t", "text", "welcome", map[string]any{"content": map[string]any{
		"vi": map[string]any{"title": strings.Repeat("x", 81)},
	}})
	if code, body := patchDesign(t, r, design(long)); code != http.StatusUnprocessableEntity ||
		!strings.Contains(body, "design.blocks.0.content.vi.title") {
		t.Errorf("an over-long title: %d\n%s", code, body)
	}
}

func TestPortalDesignRoundTripsAndIsAudited(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	// Never designed: the empty design, with `blocks` an array rather than null.
	get := r.do(http.MethodGet, "/sites/"+testSite+"/portal", nil)
	raw := readAll(t, get)
	get.Body.Close()
	if !strings.Contains(raw, `"design":{"accent":null,"blocks":[],"headline":{}}`) {
		t.Errorf("an undesigned venue does not answer the empty design:\n%s", raw)
	}

	d := design(
		block("n", "notice", "welcome", nil),
		block("b", "button", "connected", map[string]any{"url": "tel:+84 28 1234 5678"}),
	)
	d["accent"] = "#c8873a"
	d["headline"] = map[string]any{"vi": "Chào mừng"}
	code, body := patchDesign(t, r, d)
	if code != http.StatusOK {
		t.Fatalf("PATCH: %d\n%s", code, body)
	}
	var saved gen.PortalSettings
	if err := json.Unmarshal([]byte(body), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Design.Accent == nil || *saved.Design.Accent != "#c8873a" || len(saved.Design.Blocks) != 2 ||
		saved.Design.Headline.Vi == nil || *saved.Design.Headline.Vi != "Chào mừng" {
		t.Errorf("the design did not round-trip: %+v", saved.Design)
	}

	// A patch that does not mention the design leaves it alone.
	other := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{"venueName": "Renamed"})
	other.Body.Close()
	get = r.do(http.MethodGet, "/sites/"+testSite+"/portal", nil)
	var after gen.PortalSettings
	_ = json.Unmarshal([]byte(readAll(t, get)), &after)
	get.Body.Close()
	if len(after.Design.Blocks) != 2 {
		t.Errorf("a patch without a design changed it: %+v", after.Design)
	}

	// Replaced whole: sending one block leaves one block.
	if code, body := patchDesign(t, r, design(block("x", "text", "welcome", nil))); code != http.StatusOK {
		t.Fatalf("PATCH: %d\n%s", code, body)
	}
	get = r.do(http.MethodGet, "/sites/"+testSite+"/portal", nil)
	_ = json.Unmarshal([]byte(readAll(t, get)), &after)
	get.Body.Close()
	if len(after.Design.Blocks) != 1 || after.Design.Blocks[0].Id != "x" {
		t.Errorf("the design was merged rather than replaced: %+v", after.Design.Blocks)
	}

	// The audit names a design change as a settings change.
	store := r.store.World()
	changed := 0
	for _, e := range store.Audit {
		if e.Kind == gen.AuditKindPortalSettingsChanged {
			changed++
		}
	}
	if changed < 3 {
		t.Errorf("%d portal-settings-changed records for three saves, two of them design-only", changed)
	}
}

// The guest reads today's page and nothing else — not the hidden block, not last
// month's promotion, not next month's. Through the context a client renders from
// and through the page the server renders, because the two must agree.
func TestTheGuestReadsOnlyWhatIsLiveToday(t *testing.T) {
	today := at(0).Local()
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format("2006-01-02") }

	blocks := []map[string]any{
		block("live", "text", "welcome", map[string]any{"startsOn": day(0), "endsOn": day(0),
			"content": map[string]any{"en": map[string]any{"title": "Live today"}}}),
		block("hidden", "text", "welcome", map[string]any{"visible": false,
			"content": map[string]any{"en": map[string]any{"title": "Secret draft"}}}),
		block("past", "offer", "welcome", map[string]any{"endsOn": day(-1),
			"content": map[string]any{"en": map[string]any{"title": "Last month"}}}),
		block("future", "offer", "welcome", map[string]any{"startsOn": day(1),
			"content": map[string]any{"en": map[string]any{"title": "Next month"}}}),
	}

	// The context, over the guest listener.
	g := presentRig(t)
	var d gen.PortalDesign
	raw, _ := json.Marshal(design(blocks...))
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	accent := "#c8873a"
	d.Accent = &accent
	set := g.store.World().Portal[portalSite]
	set.Design = d
	g.store.World().Portal[portalSite] = set

	resp := g.arrive(guestMAC)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("getPortalContext: %d", resp.StatusCode)
	}
	text := body(t, resp)
	var ctx gen.PortalContext
	if err := json.Unmarshal([]byte(text), &ctx); err != nil {
		t.Fatal(err)
	}
	if ctx.Design == nil || len(ctx.Design.Blocks) != 1 || ctx.Design.Blocks[0].Id != "live" {
		t.Fatalf("the guest context carries the wrong blocks:\n%s", text)
	}
	if ctx.Design.Accent == nil || *ctx.Design.Accent != accent {
		t.Error("the accent did not pass through")
	}
	for _, leaked := range []string{"Secret draft", "Last month", "Next month"} {
		if strings.Contains(text, leaked) {
			t.Errorf("%q is readable pre-authentication", leaked)
		}
	}

	// The rendered page, over the documents listener.
	store := world(t)
	srv, err := api.New(api.Config{
		Ports:  store.Ports(),
		Portal: store.PortalPorts(newGuestDriver()),
		Now:    func() time.Time { return at(0) },
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	auth := httptest.NewServer(srv.Handler())
	t.Cleanup(auth.Close)
	r := &rig{t: t, server: auth, store: store}
	r.signIn(adminUser)
	dd := design(blocks...)
	dd["accent"] = accent
	if code, body := patchDesign(t, r, dd); code != http.StatusOK {
		t.Fatalf("PATCH: %d\n%s", code, body)
	}
	docs := httptest.NewServer(srv.GuestDocumentsHandler())
	t.Cleanup(docs.Close)
	page, err := docs.Client().Get(docs.URL + "/en/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(page.Body)
	page.Body.Close()
	html := string(b)
	if !strings.Contains(html, `<h2 class="block-title">Live today</h2>`) {
		t.Errorf("the live block is not on the page:\n%s", html)
	}
	if !strings.Contains(html, "<body>\n<style>:root{--c-accent:") {
		t.Error("the accent stylesheet is not the first thing in the body")
	}
	for _, leaked := range []string{"Secret draft", "Last month", "Next month"} {
		if strings.Contains(html, leaked) {
			t.Errorf("%q rendered on the guest page", leaked)
		}
	}
}
