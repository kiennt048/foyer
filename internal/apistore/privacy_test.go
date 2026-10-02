package apistore_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/store"
)

// eraseDataSubject end to end: HTTP, the gate, the adapter, the registry, both
// roles' connections and real PostgreSQL, then the audit log read back through the
// API. The criteria are that erase-by-MAC removes that MAC everywhere the operator
// document lists and that the audit log holds the record; store/privacy_test.go
// proves the first table by table, this proves the product reaches it.
func TestEraseDataSubjectEndToEnd(t *testing.T) {
	w := newWorldWith(t, func(c *apistore.Config) {
		r, err := store.OpenRetention(context.Background(), store.Config{URL: ownerURL})
		if err != nil {
			t.Fatalf("opening the retention connection: %v", err)
		}
		t.Cleanup(r.Close)
		c.Retention = r
	})
	w.signIn()
	ctx := context.Background()

	// A MAC of this world's own, in the spelling a UniFi AP sends and the one a
	// Cisco sends, so the erasure has to normalise to find both.
	mac := strings.ToUpper(strings.ReplaceAll(w.macFor(77), ":", "-"))
	for i, spelling := range []string{mac, strings.ToLower(strings.ReplaceAll(mac, "-", ""))} {
		if _, err := w.owner.Exec(ctx, `INSERT INTO radacct (acctsessionid, acctuniqueid, username, nasipaddress,
			acctstarttime, acctstoptime, callingstationid) VALUES ($1, $1, 'guest', $2, now(), now(), $3)`,
			uniq("erase"), w.block+"11", spelling); err != nil {
			t.Fatalf("seeding accounting %d: %v", i, err)
		}
		if _, err := w.owner.Exec(ctx, `INSERT INTO radpostauth (username, reply, callingstationid) VALUES ('guest', 'Access-Accept', $1)`, spelling); err != nil {
			t.Fatalf("seeding post-auth %d: %v", i, err)
		}
	}

	raw := w.do(http.MethodPost, "/privacy/erasures", map[string]any{
		"subject": map[string]any{"kind": "mac", "mac": mac}}, http.StatusOK)
	var receipt gen.DataSubjectErasure
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, r := range receipt.Removed {
		counts[r.Table] = r.Rows
	}
	if counts["radacct"] != 2 || counts["radpostauth"] != 2 {
		t.Errorf("the receipt counts %v, want two accounting and two post-auth rows", counts)
	}
	var left int
	if err := w.owner.QueryRow(ctx, `SELECT count(*) FROM radacct
		WHERE lower(regexp_replace(callingstationid, '[^0-9A-Fa-f]', '', 'g')) = $1`,
		strings.ToLower(strings.ReplaceAll(mac, "-", ""))).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d accounting rows still hold the erased MAC", left)
	}

	var page struct {
		Items []gen.AuditEntry `json:"items"`
	}
	w.getJSON("/audit?limit=200", &page)
	found := false
	for _, e := range page.Items {
		if e.Kind != gen.AuditKindDataSubjectErased || e.Subject == nil || e.Subject.Id != receipt.Id {
			continue
		}
		found = true
		entry, _ := json.Marshal(e)
		if strings.Contains(strings.ToLower(string(entry)), strings.ToLower(mac[:8])) {
			t.Errorf("the audit entry keeps the erased MAC: %s", entry)
		}
	}
	if !found {
		t.Fatalf("no data-subject-erased entry for receipt %s", receipt.Id)
	}

	// The policy reads and writes through the real tables.
	w.do(http.MethodPut, "/privacy/retention", map[string]any{
		"changes": []any{map[string]any{"category": "authAttempts", "days": 14}}}, http.StatusOK)
	var policy gen.RetentionPolicy
	w.getJSON("/privacy/retention", &policy)
	for _, c := range policy.Categories {
		if c.Category == gen.AuthAttempts && (c.Days != 14 || c.IsDefault) {
			t.Errorf("authAttempts after setting 14 days: %+v", c)
		}
	}
	w.do(http.MethodPut, "/privacy/retention", map[string]any{
		"changes": []any{map[string]any{"category": "authAttempts", "days": nil}}}, http.StatusOK)
}

// TestEraseWithoutTheRetentionConnectionRefuses: a control plane whose
// foyer_retention pool did not open must not report an erasure it could not make.
func TestEraseWithoutTheRetentionConnectionRefuses(t *testing.T) {
	w := newWorld(t)
	w.signIn()
	w.do(http.MethodPost, "/privacy/erasures", map[string]any{
		"subject": map[string]any{"kind": "mac", "mac": "02:00:5e:10:00:98"}}, http.StatusInternalServerError)
}

// TestTheContractsRetentionCategoriesAreTheStoresRegistry holds the closed enum in
// the contract to the categories the store sweeps, both ways. A category the store
// sweeps and the console cannot name is a period nobody can see or change; a member
// the console offers that the store does not sweep is a period nothing enforces.
func TestTheContractsRetentionCategoriesAreTheStoresRegistry(t *testing.T) {
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	inContract := map[string]bool{}
	for _, v := range spec.Components.Schemas["RetentionCategoryId"].Value.Enum {
		inContract[v.(string)] = true
	}
	inStore := map[string]bool{}
	for _, c := range store.RetentionCategories() {
		inStore[c.ID] = true
		if !inContract[c.ID] {
			t.Errorf("the store sweeps %q and the contract's RetentionCategoryId does not name it", c.ID)
		}
	}
	for id := range inContract {
		if !inStore[id] {
			t.Errorf("the contract names retention category %q and the store registers no sweep for it", id)
		}
	}
}
