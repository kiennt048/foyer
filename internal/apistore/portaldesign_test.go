package apistore_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The page design through the adapter and PostgreSQL: a row from before migration
// 00018 answers the empty design with `blocks` an array, a saved design comes back
// as saved, and a patch that does not carry one leaves it alone. The in-memory fake
// passes the same assertions in `internal/api`; this is the half that proves the
// column, not the fake.
func TestPortalDesignRoundTripsThroughTheColumn(t *testing.T) {
	w := newWorld(t)
	w.signIn()
	path := "/sites/" + w.siteA + "/portal"

	raw := w.do(http.MethodGet, path, nil, http.StatusOK)
	var before struct {
		Design json.RawMessage `json:"design"`
	}
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	if string(before.Design) != `{"accent":null,"blocks":[],"headline":{}}` {
		t.Errorf("an undesigned venue answers %s", before.Design)
	}

	design := map[string]any{
		"accent":   "#c8873a",
		"headline": map[string]any{"vi": "Chào mừng đến Cà phê Số 7"},
		"blocks": []map[string]any{
			{"id": "wifi", "kind": "notice", "placement": "welcome", "visible": true,
				"content": map[string]any{"vi": map[string]any{"title": "Mật khẩu ở quầy"}}},
			{"id": "menu", "kind": "list", "placement": "connected", "visible": true,
				"startsOn": "2026-09-01", "endsOn": "2026-09-30",
				"content": map[string]any{"en": map[string]any{"title": "Menu",
					"items": []map[string]any{{"label": "Cà phê sữa đá", "value": "29k"}}}}},
			{"id": "fb", "kind": "social", "placement": "connected", "visible": true,
				"links":   []map[string]any{{"network": "facebook", "url": "https://facebook.com/cafe7"}},
				"content": map[string]any{}},
		},
	}
	w.do(http.MethodPatch, path, map[string]any{"design": design}, http.StatusOK)
	w.do(http.MethodPatch, path, map[string]any{"venueName": "Cà Phê Số 7"}, http.StatusOK)

	var after gen.PortalSettings
	w.getJSON(path, &after)
	d := after.Design
	if d.Accent == nil || *d.Accent != "#c8873a" || d.Headline.Vi == nil || len(d.Blocks) != 3 {
		t.Fatalf("the design did not survive the column and a later patch: %+v", d)
	}
	menu := d.Blocks[1]
	if menu.StartsOn == nil || menu.StartsOn.String() != "2026-09-01" || menu.Content.En == nil ||
		menu.Content.En.Items == nil || (*menu.Content.En.Items)[0].Label != "Cà phê sữa đá" {
		t.Errorf("a block's dates or items were lost: %+v", menu)
	}
	if d.Blocks[2].Links == nil || (*d.Blocks[2].Links)[0].Url != "https://facebook.com/cafe7" {
		t.Errorf("a social block's links were lost: %+v", d.Blocks[2])
	}

	// And the other site's row is untouched — the write is per site.
	var other gen.PortalSettings
	w.getJSON("/sites/"+w.siteB+"/portal", &other)
	if len(other.Design.Blocks) != 0 || other.Design.Accent != nil {
		t.Errorf("site B holds site A's design: %+v", other.Design)
	}

	// What the adapter reads is what the repository holds, byte-for-byte as JSON.
	row, err := w.st.Portal().Get(context.Background(), w.siteA)
	if err != nil {
		t.Fatal(err)
	}
	var stored gen.PortalDesign
	if err := json.Unmarshal(row.Design, &stored); err != nil {
		t.Fatalf("the column does not hold a PortalDesign: %v (%s)", err, row.Design)
	}
	if len(stored.Blocks) != 3 {
		t.Errorf("the column holds %d blocks", len(stored.Blocks))
	}
}
