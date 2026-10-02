package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// The three privacy operations at the HTTP boundary, against the memory fake:
// who may call them, what they refuse, and what they write to the audit log. The
// erasure itself is proven against PostgreSQL in internal/store and
// internal/apistore.

func TestPrivacyOperationsAreAdministratorsOnly(t *testing.T) {
	r := newRig(t)
	r.signIn(staffUser)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/privacy/retention", nil},
		{http.MethodPut, "/privacy/retention", map[string]any{"changes": []any{map[string]any{"category": "accounting", "days": 1}}}},
		{http.MethodPost, "/privacy/erasures", map[string]any{"subject": map[string]any{"kind": "mac", "mac": "02:00:00:00:00:01"}}},
	} {
		resp := r.do(c.method, c.path, c.body)
		body := readAll(t, resp)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("staff %s %s: %d, want 403: %s", c.method, c.path, resp.StatusCode, body)
		}
	}
}

func TestRetentionPolicyListsEveryCategoryWithItsDefault(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)
	resp := r.do(http.MethodGet, "/privacy/retention", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, readAll(t, resp))
	}
	var p gen.RetentionPolicy
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	got := map[gen.RetentionCategoryId]gen.RetentionCategoryPolicy{}
	for _, c := range p.Categories {
		got[c.Category] = c
	}
	for _, id := range []gen.RetentionCategoryId{gen.Accounting, gen.AuthAttempts, gen.PortalGrants, gen.Audit, gen.GuestAccounts} {
		c, ok := got[id]
		if !ok {
			t.Errorf("%s is missing from the policy", id)
			continue
		}
		if !c.IsDefault || c.Days != c.DefaultDays {
			t.Errorf("%s on a fresh deployment: %+v", id, c)
		}
	}
}

func TestRetentionChangesAreBoundedAndAudited(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	// Out of range both ways, with the figure the client renders.
	for _, c := range []struct {
		days  int
		fault gen.FieldFault
		param string
	}{{0, gen.FieldFaultBelowMinimum, "minimumCount"}, {3651, gen.FieldFaultAboveMaximum, "maximumCount"}} {
		resp := r.do(http.MethodPut, "/privacy/retention", map[string]any{
			"changes": []any{map[string]any{"category": "accounting", "days": c.days}}})
		body := readAll(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%d days: %d, want 422: %s", c.days, resp.StatusCode, body)
		}
		if !strings.Contains(body, string(c.fault)) || !strings.Contains(body, c.param) {
			t.Errorf("%d days: the refusal does not carry %s and %s: %s", c.days, c.fault, c.param, body)
		}
	}

	resp := r.do(http.MethodPut, "/privacy/retention", map[string]any{
		"changes": []any{map[string]any{"category": "accounting", "days": 30}}})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a valid change: %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"days":30`) {
		t.Errorf("the response does not show the new period: %s", body)
	}

	entries := auditOf(t, r, gen.AuditKindRetentionPolicyChanged)
	if len(entries) != 1 {
		t.Fatalf("%d retention-policy-changed entries, want 1", len(entries))
	}
	params, _ := json.Marshal(entries[0].Params)
	if !strings.Contains(string(params), `"accounting":{"from":90,"to":30}`) {
		t.Errorf("the entry does not record the old and new period: %s", params)
	}
	if entries[0].SiteId != nil {
		t.Error("a retention change is the deployment's, and the entry names a venue")
	}
}

func TestErasureIsAuditedWithTheSubjectMasked(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	resp := r.do(http.MethodPost, "/privacy/erasures", map[string]any{
		"subject": map[string]any{"kind": "mac", "mac": "AA-BB-CC-00-11-22"}})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	var receipt gen.DataSubjectErasure
	if err := json.Unmarshal([]byte(body), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Id == "" || len(receipt.Removed) == 0 {
		t.Fatalf("receipt: %+v", receipt)
	}

	entries := auditOf(t, r, gen.AuditKindDataSubjectErased)
	if len(entries) != 1 {
		t.Fatalf("%d data-subject-erased entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Subject == nil || e.Subject.Id != receipt.Id || e.Subject.Kind != gen.AuditSubjectKindDataSubject {
		t.Errorf("the entry's subject does not match the receipt: %+v", e.Subject)
	}
	raw, _ := json.Marshal(e)
	for _, spelling := range []string{"aa:bb:cc", "AA-BB-CC", "aabbcc", "AABBCC"} {
		if strings.Contains(string(raw), spelling) {
			t.Errorf("the audit entry keeps the erased MAC (%s): %s", spelling, raw)
		}
	}
}

func TestErasureRefusesWhatIsNotASubject(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)
	for _, c := range []struct {
		subject map[string]any
		status  int
		want    string
	}{
		{map[string]any{"kind": "mac", "mac": "user-cafe00112233"}, http.StatusUnprocessableEntity, string(gen.FieldFaultNotAMacAddress)},
		{map[string]any{"kind": "mac"}, http.StatusUnprocessableEntity, string(gen.FieldFaultRequired)},
		{map[string]any{"kind": "account", "accountId": "acc-nobody"}, http.StatusNotFound, "not-found"},
		{map[string]any{"kind": "phone"}, http.StatusUnprocessableEntity, string(gen.FieldFaultRequired)},
		{map[string]any{"kind": "phone", "phone": "guest@example.com"}, http.StatusUnprocessableEntity, string(gen.FieldFaultNotAPhoneNumber)},
		{map[string]any{"kind": "phone", "phone": "1234"}, http.StatusUnprocessableEntity, string(gen.FieldFaultNotAPhoneNumber)},
		{map[string]any{"kind": "email"}, http.StatusUnprocessableEntity, string(gen.FieldFaultRequired)},
		{map[string]any{"kind": "email", "email": "0912 345 678"}, http.StatusUnprocessableEntity, string(gen.FieldFaultNotAnEmailAddress)},
	} {
		resp := r.do(http.MethodPost, "/privacy/erasures", map[string]any{"subject": c.subject})
		body := readAll(t, resp)
		if resp.StatusCode != c.status || !strings.Contains(body, c.want) {
			t.Errorf("%v: %d %s, want %d mentioning %s", c.subject, resp.StatusCode, body, c.status, c.want)
		}
	}
	if n := len(auditOf(t, r, gen.AuditKindDataSubjectErased)); n != 0 {
		t.Errorf("a refused erasure wrote %d audit entries", n)
	}
}

func auditOf(t *testing.T, r *rig, kind gen.AuditKind) []gen.AuditEntry {
	t.Helper()
	page, err := r.store.Audit(context.Background(), api.AuditQuery{Window: api.Window{Limit: 200}})
	if err != nil {
		t.Fatal(err)
	}
	var out []gen.AuditEntry
	for _, e := range page.Items {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// TestAnErasureByPhoneNumberNamesItMasked: a phone guest's number is an identifier
// they can give, so an erasure can be asked for by it — and like a
// MAC, the receipt and the audit entry keep only its last three figures.
func TestAnErasureByPhoneNumberNamesItMasked(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)
	resp := r.do(http.MethodPost, "/privacy/erasures",
		map[string]any{"subject": map[string]any{"kind": "phone", "phone": "0912 345 678"}})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var receipt gen.DataSubjectErasure
	if err := json.Unmarshal([]byte(body), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Subject.Kind != gen.DataSubjectKindPhone || receipt.Subject.Masked != "••••••••678" {
		t.Errorf("subject %+v, want the number masked to its last three figures", receipt.Subject)
	}
	if strings.Contains(body, "345") {
		t.Errorf("the receipt carries the number: %s", body)
	}
	var tables []string
	for _, e := range receipt.Removed {
		tables = append(tables, e.Table)
	}
	for _, want := range []string{"phone_codes", "portal_grants", "guest_details", "marketing_consents", "authorization_events", "radacct"} {
		if !slices.Contains(tables, want) {
			t.Errorf("the receipt does not say it searched %s: %v", want, tables)
		}
	}
	entries := auditOf(t, r, gen.AuditKindDataSubjectErased)
	if len(entries) != 1 || entries[0].Subject.Name == nil || *entries[0].Subject.Name != "••••••••678" {
		t.Errorf("audit entries %+v, want one naming the number masked", entries)
	}
}

// TestAnErasureByEmailIsMaskedAndPseudonymisesTheLog: the fourth subject kind at
// the boundary — masked in the receipt and the entry, and the earlier entry naming
// the guest kept with the address replaced.
func TestAnErasureByEmailIsMaskedAndPseudonymisesTheLog(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)
	name := "Lan.Tran@Example.vn"
	if err := r.store.Append(context.Background(), gen.AuditEntry{Id: "aud-mc", Kind: gen.AuditKindMarketingConsentWithdrawn,
		ActorId: "op-1", ActorUsername: "linh", Subject: &gen.AuditSubject{Kind: gen.AuditSubjectKindMarketingConsent, Id: "mc-9", Name: &name}}); err != nil {
		t.Fatal(err)
	}
	resp := r.do(http.MethodPost, "/privacy/erasures",
		map[string]any{"subject": map[string]any{"kind": "email", "email": "lan.tran@example.vn"}})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var receipt gen.DataSubjectErasure
	if err := json.Unmarshal([]byte(body), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Subject.Masked != "l•••@•••.vn" || strings.Contains(strings.ToLower(body), "lan.tran") {
		t.Errorf("receipt %s", body)
	}
	if len(receipt.Pseudonymised) != 1 || receipt.Pseudonymised[0].Table != "audit_log" || receipt.Pseudonymised[0].Rows != 1 {
		t.Errorf("pseudonymised %+v, want the one audit entry", receipt.Pseudonymised)
	}
	for _, e := range auditOf(t, r, gen.AuditKindMarketingConsentWithdrawn) {
		if e.Subject != nil && e.Subject.Name != nil && strings.EqualFold(*e.Subject.Name, name) {
			t.Error("the earlier entry still names the erased address")
		}
	}
	entries := auditOf(t, r, gen.AuditKindDataSubjectErased)
	if len(entries) != 1 || entries[0].Subject.Name == nil || *entries[0].Subject.Name != "l•••@•••.vn" {
		t.Fatalf("erasure entries %+v", entries)
	}
	params, _ := json.Marshal(entries[0].Params)
	if !strings.Contains(string(params), `"pseudonymised":{"audit_log":1}`) {
		t.Errorf("the entry does not say what it pseudonymised: %s", params)
	}
}
