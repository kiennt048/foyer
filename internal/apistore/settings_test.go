package apistore_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/store"
)

// Every setting is configurable from the console, end to end over the real adapter
// and PostgreSQL: the console's write is what the running process uses
// next, and the write-only secret never comes back from a settings operation.

// TestAMaintenanceWriteReachesTheRunningScheduler: the values saved are handed to
// the process's scheduler hook (cmd/foyer wires it to `Scheduler.Reschedule`) and
// are what the next read -- the jobs' own, every cycle -- answers.
func TestAMaintenanceWriteReachesTheRunningScheduler(t *testing.T) {
	var mu sync.Mutex
	var pushed []store.MaintenanceValues
	w := newWorldWith(t, func(c *apistore.Config) {
		c.MaintenanceChanged = func(v store.MaintenanceValues) {
			mu.Lock()
			defer mu.Unlock()
			pushed = append(pushed, v)
		}
	})
	// One row per tenant, shared by every world in this package: put it back.
	t.Cleanup(func() { _, _ = w.st.Maintenance().Set(context.Background(), store.DefaultMaintenance(), "") })
	w.signIn()

	w.do(http.MethodPut, "/privacy/maintenance", map[string]any{
		"intervalSeconds": 120, "staleAfterMissedUpdates": 5, "staleCeilingPerPass": 25, "retentionBatchSize": 250,
	}, http.StatusOK)

	want := store.MaintenanceValues{IntervalSeconds: 120, StaleAfterMissedUpdates: 5, StaleCeilingPerPass: 25, RetentionBatchSize: 250}
	mu.Lock()
	got := append([]store.MaintenanceValues(nil), pushed...)
	mu.Unlock()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("the scheduler hook was handed %+v, want exactly %+v", got, want)
	}
	row, err := w.st.Maintenance().Get(context.Background())
	if err != nil || row.MaintenanceValues != want || row.UpdatedBy == nil || *row.UpdatedBy != w.adminUser {
		t.Fatalf("the jobs' next read answers %+v, %v", row, err)
	}

	// Refused out of range, and the hook is not called for a refusal.
	w.do(http.MethodPut, "/privacy/maintenance", map[string]any{
		"intervalSeconds": 30, "staleAfterMissedUpdates": 5, "staleCeilingPerPass": 25, "retentionBatchSize": 250,
	}, http.StatusUnprocessableEntity)
	mu.Lock()
	defer mu.Unlock()
	if len(pushed) != 1 {
		t.Fatalf("a refused write reached the scheduler: %+v", pushed)
	}
}

// TestTheGuestWifiPasswordNeverComesBackFromItsSettings: set over the console's
// surface, absent from every settings answer, present in the slip's read.
func TestTheGuestWifiPasswordNeverComesBackFromItsSettings(t *testing.T) {
	w := newWorld(t)
	w.signIn()
	const secret = "Pho-Bo-2026!"
	path := "/sites/" + w.siteA + "/guest-wifi-key"

	put := w.do(http.MethodPut, path, map[string]any{"passphrase": secret}, http.StatusOK)
	get := w.do(http.MethodGet, path, nil, http.StatusOK)
	for _, body := range [][]byte{put, get} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("a settings answer carries the password: %s", body)
		}
	}
	var st gen.GuestWifiKeyStatus
	if err := json.Unmarshal(get, &st); err != nil || !st.Set {
		t.Fatalf("status after set: %s", get)
	}
	var slip gen.VoucherSlipWifi
	if err := json.Unmarshal(w.do(http.MethodGet, "/sites/"+w.siteA+"/voucher-slip-wifi", nil, http.StatusOK), &slip); err != nil ||
		slip.Passphrase == nil || *slip.Passphrase != secret {
		t.Fatalf("the slip reads %+v, %v", slip, err)
	}
	// Another venue's slip does not.
	var other gen.VoucherSlipWifi
	_ = json.Unmarshal(w.do(http.MethodGet, "/sites/"+w.siteB+"/voucher-slip-wifi", nil, http.StatusOK), &other)
	if other.Passphrase != nil {
		t.Fatal("one venue's Wi-Fi password printed on another venue's slip")
	}
}

// TestAnAccountsEightOhTwoOneXOptInOverTheAdapter: `AmendAccount`'s decision --
// what the request asks against what the account holds -- is the adapter's, not
// the memory fake's, so it is driven here against the real verifiers.
func TestAnAccountsEightOhTwoOneXOptInOverTheAdapter(t *testing.T) {
	w := newWorld(t)
	w.signIn()
	var acc gen.Account
	if err := json.Unmarshal(w.do(http.MethodPost, "/accounts", map[string]any{
		"siteId": w.siteA, "username": uniq("dot1x"), "password": "a-long-enough-password", "eapTtlsPap": true,
	}, http.StatusCreated), &acc); err != nil || acc.CredentialKind != gen.Argon2idBcrypt {
		t.Fatalf("created opted in: %+v %v", acc, err)
	}
	path := "/accounts/" + acc.Id

	off := w.do(http.MethodPatch, path, map[string]any{"eapTtlsPap": false}, http.StatusOK)
	if !strings.Contains(string(off), `"credentialKind":"argon2id"`) {
		t.Fatalf("after the opt-out: %s", off)
	}
	before, err := w.st.Accounts().Get(context.Background(), acc.Id)
	if err != nil {
		t.Fatal(err)
	}
	refused := w.do(http.MethodPatch, path, map[string]any{"eapTtlsPap": true}, http.StatusUnprocessableEntity)
	if !strings.Contains(string(refused), `"password":"required"`) {
		t.Fatalf("an opt-in with no password: %s", refused)
	}
	on := w.do(http.MethodPatch, path, map[string]any{"eapTtlsPap": true, "password": "a-new-long-password"}, http.StatusOK)
	if !strings.Contains(string(on), `"credentialKind":"argon2id+bcrypt"`) {
		t.Fatalf("after the opt-in: %s", on)
	}
	after, err := w.st.Accounts().Get(context.Background(), acc.Id)
	if err != nil || after.PasswordHash == before.PasswordHash {
		t.Fatal("the opt-in with a new password did not replace the argon2id verifier as well")
	}
}
