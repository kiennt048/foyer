package apistore_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/store"
)

// End to end: an erasure or a retention change whose audit entry cannot be written is refused and changes
// nothing — the handler, the adapter and the store's transaction together. The
// audit log is made unwritable the way a real fault would make it: PostgreSQL
// refuses the INSERT. A `recordIn` that swallowed that error after the change had
// committed would leave an irreversible change with no record of it.
func TestAnIrreversiblePrivacyChangeTheLogCannotRecordIsNotMade(t *testing.T) {
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
	site := w.siteA
	acc := uniq("acc")
	if _, err := w.owner.Exec(ctx, `INSERT INTO accounts (id, site_id, username, password_hash)
		VALUES ($1, $2, $3, '$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA')`, acc, site, uniq("unlogged")); err != nil {
		t.Fatalf("seeding an account: %v", err)
	}

	unwritable := func() {
		t.Helper()
		if _, err := w.owner.Exec(ctx, `REVOKE INSERT ON audit_log FROM foyer_app`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = w.owner.Exec(context.Background(), `GRANT INSERT ON audit_log TO foyer_app`) })
	}
	var before gen.RetentionPolicy
	w.getJSON("/privacy/retention", &before)
	unwritable()

	w.do(http.MethodPost, "/privacy/erasures", map[string]any{
		"subject": map[string]any{"kind": "account", "accountId": acc}}, http.StatusInternalServerError)
	var n int
	if err := w.owner.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE id = $1`, acc).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("the account was erased although the erasure could not be recorded")
	}

	w.do(http.MethodPut, "/privacy/retention", map[string]any{
		"changes": []any{map[string]any{"category": "audit", "days": 30}}}, http.StatusInternalServerError)
	var after gen.RetentionPolicy
	w.getJSON("/privacy/retention", &after)
	for i, c := range after.Categories {
		if c.Category == gen.Audit && c.Days != before.Categories[i].Days {
			t.Errorf("the audit period changed from %d to %d with no record of who changed it", before.Categories[i].Days, c.Days)
		}
	}
}

// TestEraseByEmailEndToEnd: the fourth subject kind through HTTP, with the audit
// log's earlier entry about the guest pseudonymised rather than deleted.
func TestEraseByEmailEndToEnd(t *testing.T) {
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
	site := w.siteA
	email := uniq("lan") + "@Example.test"
	grant := uniq("grant")
	if _, err := w.owner.Exec(ctx, `INSERT INTO portal_grants (id, site_id, client_mac, method, enforced_by, granted_at)
		VALUES ($1, $2, '02:00:5e:10:00:99', 'click-through', 'unenforced', now())`, grant, site); err != nil {
		t.Fatalf("seeding a grant: %v", err)
	}
	if _, err := w.owner.Exec(ctx, `INSERT INTO guest_details (grant_id, site_id, email) VALUES ($1, $2, $3)`, grant, site, email); err != nil {
		t.Fatalf("seeding details: %v", err)
	}
	if _, err := w.owner.Exec(ctx, `INSERT INTO audit_log (id, actor_id, actor_username, kind, subject_kind, subject_id, params)
		VALUES ($1, 'op-1', 'linh', 'marketing-consent-withdrawn', 'marketing-consent', 'mc-1', jsonb_build_object('email', $2::text))`,
		uniq("aud"), email); err != nil {
		t.Fatalf("seeding the audit entry: %v", err)
	}

	w.do(http.MethodPost, "/privacy/erasures", map[string]any{
		"subject": map[string]any{"kind": "email", "email": "not an address"}}, http.StatusUnprocessableEntity)
	raw := w.do(http.MethodPost, "/privacy/erasures", map[string]any{
		"subject": map[string]any{"kind": "email", "email": email}}, http.StatusOK)
	var receipt gen.DataSubjectErasure
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Total != 1 || len(receipt.Pseudonymised) != 1 || receipt.Pseudonymised[0].Rows != 1 {
		t.Errorf("receipt %+v", receipt)
	}
	var left int
	if err := w.owner.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE params::text ILIKE '%' || $1 || '%'`, email).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Error("the audit log still carries the erased address")
	}
}
