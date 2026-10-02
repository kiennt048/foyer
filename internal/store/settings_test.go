package store_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// The admin-configurability rule at the database — every operator setting is set
// in the admin console and takes effect without a restart: the maintenance
// settings persist and are bounded, the guest Wi-Fi password is sealed,
// and an account's two verifiers change together or not at all.

func TestMaintenanceSettingsPersistAndAreBounded(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()

	v := store.DefaultMaintenance()
	v.IntervalSeconds, v.StaleAfterMissedUpdates, v.StaleCeilingPerPass, v.RetentionBatchSize = 900, 6, 50, 100
	got, err := st.Maintenance().Set(ctx, v, "kim.administrator")
	fatalf(t, err, "set")
	if got.IsDefault || got.MaintenanceValues != v || got.UpdatedBy == nil || *got.UpdatedBy != "kim.administrator" {
		t.Fatalf("after set: %+v", got)
	}
	// A second process reads the same row.
	other := appStore(t)
	if again, err := other.Maintenance().Get(ctx); err != nil || again.MaintenanceValues != v {
		t.Fatalf("another connection reads %+v, %v", again, err)
	}

	for _, bad := range []store.MaintenanceValues{
		{IntervalSeconds: 59, StaleAfterMissedUpdates: 4, StaleCeilingPerPass: 200, RetentionBatchSize: 500},
		{IntervalSeconds: 300, StaleAfterMissedUpdates: 2, StaleCeilingPerPass: 200, RetentionBatchSize: 500},
		{IntervalSeconds: 300, StaleAfterMissedUpdates: 4, StaleCeilingPerPass: 0, RetentionBatchSize: 500},
		{IntervalSeconds: 300, StaleAfterMissedUpdates: 4, StaleCeilingPerPass: 200, RetentionBatchSize: 10001},
	} {
		if _, err := st.Maintenance().Set(ctx, bad, ""); !errors.Is(err, store.ErrMaintenanceOutOfRange) {
			t.Errorf("%+v: %v, want ErrMaintenanceOutOfRange", bad, err)
		}
	}
	// The CHECK is the backstop for a writer that skipped the Go bounds.
	if _, err := ownerPool(t).Exec(ctx, `UPDATE maintenance_settings SET stale_after_missed_updates = 2`); err == nil {
		t.Error("the database accepted a reaper threshold at the orphan-display threshold")
	}
	_, err = st.Maintenance().Set(ctx, store.DefaultMaintenance(), "")
	fatalf(t, err, "restoring the defaults")
}

func TestTheGuestWifiPasswordIsSealedAndWriteOnly(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	const secret = "Pho-Bo-2026!"

	if s, err := st.GuestWifiKeys().Status(ctx, site.ID); err != nil || s.Set {
		t.Fatalf("a new site: %+v %v", s, err)
	}
	if err := st.GuestWifiKeys().Set(ctx, site.ID, "short", ""); !errors.Is(err, store.ErrNotAWifiPassphrase) {
		t.Fatalf("a 5-character passphrase: %v", err)
	}
	fatalf(t, st.GuestWifiKeys().Set(ctx, site.ID, secret, "kim.administrator"), "set")

	var raw []byte
	fatalf(t, ownerPool(t).QueryRow(ctx, `SELECT passphrase FROM guest_wifi_keys WHERE site_id = $1`, site.ID).Scan(&raw), "raw read")
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("the guest Wi-Fi password is at rest in the clear")
	}
	if s, err := st.GuestWifiKeys().Status(ctx, site.ID); err != nil || !s.Set || s.UpdatedAt == nil {
		t.Fatalf("status after set: %+v %v", s, err)
	}
	if p, err := st.GuestWifiKeys().Reveal(ctx, site.ID); err != nil || p != secret {
		t.Fatalf("reveal: %q %v", p, err)
	}

	radius := storeAsRole(t, "foyer_radius", store.DefaultTenantID)
	if _, err := radius.Pool().Exec(ctx, `SELECT passphrase FROM guest_wifi_keys`); err == nil ||
		!strings.Contains(err.Error(), "guest_wifi_keys") {
		t.Fatalf("foyer_radius read guest_wifi_keys: %v", err)
	}

	fatalf(t, st.GuestWifiKeys().Clear(ctx, site.ID), "clear")
	if p, err := st.GuestWifiKeys().Reveal(ctx, site.ID); err != nil || p != "" {
		t.Fatalf("after clear: %q %v", p, err)
	}
}

func TestAnAccountsTwoVerifiersChangeTogetherOrNotAtAll(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	cafe, hotel := seedSite(t, st), seedSite(t, st)
	shared := uniq("u")
	inCafe := seedAccount(t, st, cafe.ID, shared)
	inHotel := seedAccount(t, st, hotel.ID, shared)

	pap, err := identity.HashPAPVerifier("the-cafe-password")
	fatalf(t, err, "bcrypt")
	hash, err := identity.HashPassword("the-cafe-password")
	fatalf(t, err, "argon2id")
	acc, err := st.Accounts().SetCredentials(ctx, inCafe.ID, store.CredentialChange{PasswordHash: &hash, PAPVerifier: &pap})
	fatalf(t, err, "opt the café's account in")
	if acc.CredentialKind != "argon2id+bcrypt" || acc.PasswordHash != hash {
		t.Fatalf("after the opt-in: %s", acc.CredentialKind)
	}

	// The hotel's namesake cannot opt in -- and its password must not change either.
	before, err := st.Accounts().Get(ctx, inHotel.ID)
	fatalf(t, err, "get")
	other, err := identity.HashPassword("the-hotel-password")
	fatalf(t, err, "argon2id")
	_, err = st.Accounts().SetCredentials(ctx, inHotel.ID, store.CredentialChange{PasswordHash: &other, PAPVerifier: &pap})
	if !errors.Is(err, store.ErrPAPUsernameTaken) {
		t.Fatalf("a second opt-in under one 802.1X username: %v", err)
	}
	after, err := st.Accounts().Get(ctx, inHotel.ID)
	fatalf(t, err, "get")
	if after.PasswordHash != before.PasswordHash || after.CredentialKind != "argon2id" {
		t.Fatal("a refused opt-in changed the password anyway")
	}

	acc, err = st.Accounts().SetCredentials(ctx, inCafe.ID, store.CredentialChange{OptOut8021X: true})
	fatalf(t, err, "opt out")
	if acc.CredentialKind != "argon2id" {
		t.Fatalf("after the opt-out: %s", acc.CredentialKind)
	}

	// Created opted in, in one transaction.
	fresh := &store.Account{ID: uniq("acc"), SiteID: cafe.ID, Username: uniq("u"), PasswordHash: hash, Enabled: true}
	fatalf(t, st.Accounts().CreateWithCredentials(ctx, fresh, pap, nil), "create opted in")
	if got, _ := st.Accounts().Get(ctx, fresh.ID); got == nil || got.CredentialKind != "argon2id+bcrypt" {
		t.Fatalf("created opted in: %+v", got)
	}
}
