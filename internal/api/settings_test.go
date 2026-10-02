package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The admin-configurability rule's operations at the HTTP boundary, against the
// memory fake: a write is what the next read answers, bounds carry their figure,
// the passphrase never comes back from the settings operations, and each write is
// audited without its value. That the schedule reaches the running jobs is proven
// in internal/jobs and cmd/foyer; that the rows persist, in internal/store.

func TestMaintenanceSettingsWriteIsWhatTheNextReadAnswers(t *testing.T) {
	r := newRig(t)
	r.signIn(staffUser)
	if resp := r.do(http.MethodGet, "/privacy/maintenance", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("staff read the maintenance settings: %d, want 403", resp.StatusCode)
	}
	r = newRig(t)
	r.signIn(adminUser)

	resp := r.do(http.MethodGet, "/privacy/maintenance", nil)
	var before gen.MaintenanceSettings
	if err := json.Unmarshal([]byte(readAll(t, resp)), &before); err != nil {
		t.Fatal(err)
	}
	if !before.IsDefault || before.IntervalSeconds != 300 || before.Limits.IntervalSeconds.Minimum != 60 {
		t.Fatalf("a fresh deployment's settings: %+v", before)
	}

	// Below and above, each with the figure the console renders.
	bad := map[string]any{"intervalSeconds": 5, "staleAfterMissedUpdates": 2, "staleCeilingPerPass": 200, "retentionBatchSize": 20000}
	resp = r.do(http.MethodPut, "/privacy/maintenance", bad)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("out of range: %d: %s", resp.StatusCode, body)
	}
	for _, want := range []string{`"intervalSeconds":{"minimumCount":60}`, `"staleAfterMissedUpdates":{"minimumCount":3}`, `"retentionBatchSize":{"maximumCount":10000}`} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not carry %s: %s", want, body)
		}
	}

	good := map[string]any{"intervalSeconds": 900, "staleAfterMissedUpdates": 6, "staleCeilingPerPass": 50, "retentionBatchSize": 100}
	if resp := r.do(http.MethodPut, "/privacy/maintenance", good); resp.StatusCode != http.StatusOK {
		t.Fatalf("a valid write: %d: %s", resp.StatusCode, readAll(t, resp))
	}
	var after gen.MaintenanceSettings
	if err := json.Unmarshal([]byte(readAll(t, r.do(http.MethodGet, "/privacy/maintenance", nil))), &after); err != nil {
		t.Fatal(err)
	}
	if after.IsDefault || after.IntervalSeconds != 900 || after.StaleAfterMissedUpdates != 6 ||
		after.StaleCeilingPerPass != 50 || after.RetentionBatchSize != 100 || after.UpdatedBy == nil || *after.UpdatedBy != adminUser {
		t.Fatalf("the next read answers %+v", after)
	}

	entries := auditOf(t, r, gen.AuditKindMaintenanceSettingsChanged)
	if len(entries) != 1 {
		t.Fatalf("%d maintenance-settings-changed entries, want 1", len(entries))
	}
	params, _ := json.Marshal(entries[0].Params)
	if !strings.Contains(string(params), `"intervalSeconds":{"from":300,"to":900}`) || entries[0].SiteId != nil {
		t.Errorf("the entry: %s, site %v", params, entries[0].SiteId)
	}
}

func TestGuestWifiKeyIsWriteOnlyAndOnlyTheSlipReadsIt(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)
	const secret = "Pho-Bo-2026!"
	path := "/sites/" + testSite + "/guest-wifi-key"

	for _, p := range []string{"short", strings.Repeat("x", 64), "mật-khẩu-wifi"} {
		resp := r.do(http.MethodPut, path, map[string]any{"passphrase": p})
		body := readAll(t, resp)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, string(gen.FieldFaultNotAWifiPassphrase)) {
			t.Errorf("%q: %d %s, want 422 not-a-wifi-passphrase", p, resp.StatusCode, body)
		}
	}

	resp := r.do(http.MethodPut, path, map[string]any{"passphrase": secret})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"set":true`) {
		t.Fatalf("set: %d %s", resp.StatusCode, body)
	}
	status := readAll(t, r.do(http.MethodGet, path, nil))
	if strings.Contains(body, secret) || strings.Contains(status, secret) || strings.Contains(status, "passphrase") {
		t.Fatalf("a settings operation returned the passphrase: %s / %s", body, status)
	}

	slip := readAll(t, r.do(http.MethodGet, "/sites/"+testSite+"/voucher-slip-wifi", nil))
	if !strings.Contains(slip, `"passphrase":"`+secret+`"`) {
		t.Fatalf("the slip's read does not carry the passphrase: %s", slip)
	}

	entries := auditOf(t, r, gen.AuditKindPortalSettingsChanged)
	if len(entries) != 1 {
		t.Fatalf("%d portal-settings-changed entries, want 1", len(entries))
	}
	params, _ := json.Marshal(entries[0].Params)
	if strings.Contains(string(params), secret) || !strings.Contains(string(params), "guestWifiKey") {
		t.Errorf("the audit entry: %s", params)
	}

	if resp := r.do(http.MethodPut, path, map[string]any{"passphrase": nil}); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: %d", resp.StatusCode)
	}
	if slip := readAll(t, r.do(http.MethodGet, "/sites/"+testSite+"/voucher-slip-wifi", nil)); !strings.Contains(slip, `"passphrase":null`) {
		t.Errorf("after clearing, the slip reads %s", slip)
	}

	r.signIn(staffUser)
	if resp := r.do(http.MethodPut, path, map[string]any{"passphrase": secret}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("staff set the guest Wi-Fi password: %d, want 403", resp.StatusCode)
	}
}

func TestAnAccountsEightOhTwoOneXOptInTravelsWithItsPassword(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)
	resp := r.do(http.MethodPost, "/accounts", map[string]any{
		"siteId": testSite, "username": "lan.nguyen", "password": "a-long-enough-password"})
	var acc gen.Account
	if err := json.Unmarshal([]byte(readAll(t, resp)), &acc); err != nil || acc.CredentialKind != gen.Argon2id {
		t.Fatalf("create: %v %+v", err, acc)
	}
	path := "/accounts/" + acc.Id

	// On, with no password to make the verifier from: refused on `password`.
	resp = r.do(http.MethodPatch, path, map[string]any{"eapTtlsPap": true})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `"password":"required"`) {
		t.Fatalf("opt-in without a password: %d %s", resp.StatusCode, body)
	}
	// Longer than bcrypt reads, with the figure.
	resp = r.do(http.MethodPatch, path, map[string]any{"eapTtlsPap": true, "password": strings.Repeat("mật", 20)})
	body = readAll(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `"maximumCount":72`) {
		t.Fatalf("opt-in with 100 bytes: %d %s", resp.StatusCode, body)
	}

	resp = r.do(http.MethodPatch, path, map[string]any{"eapTtlsPap": true, "password": "another-long-password"})
	body = readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"credentialKind":"argon2id+bcrypt"`) {
		t.Fatalf("opt-in with a password: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "another-long-password") {
		t.Fatal("the response carries the password")
	}

	resp = r.do(http.MethodPatch, path, map[string]any{"eapTtlsPap": false})
	if body := readAll(t, resp); !strings.Contains(body, `"credentialKind":"argon2id"`) {
		t.Fatalf("opt-out: %s", body)
	}

	entries := auditOf(t, r, gen.AuditKindAccountCredentialsChanged)
	if len(entries) != 2 {
		t.Fatalf("%d account-credentials-changed entries, want 2 (the refusals write none)", len(entries))
	}
	for _, e := range entries {
		params, _ := json.Marshal(e.Params)
		if strings.Contains(string(params), "password\":\"") || e.SiteId == nil || *e.SiteId != testSite {
			t.Errorf("the entry: %s, site %v", params, e.SiteId)
		}
	}
}
