package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

// Guest-data consent against real PostgreSQL: a consent is stored only as a row a
// ticked box wrote, withdrawal clears the contact and keeps the evidence, erasure
// removes it, and the retention sweep ages it out.

const consentText = "Gửi ưu đãi của quán cho tôi qua email."

func publishConsent(t *testing.T, w *privacyWorld) string {
	t.Helper()
	err := w.app.GuestData().PutSettings(context.Background(), &store.GuestDataSettings{
		SiteID: w.site.ID, ConsentEnabled: true,
		Fields:      []store.GuestDataFieldSetting{{Field: store.FieldEmail, Required: true}},
		ConsentText: map[string]string{"vi": "  " + consentText + "\n"},
	})
	fatalf(t, err, "publishing the consent text")
	return store.ConsentVersion(consentText)
}

func strp(s string) *string { return &s }

// TestAMarketingConsentIsStoredAndWithdrawn: the row carries the exact sentence and
// its language, a version the venue never published stores nothing, and a
// withdrawal — the guest's or an operator's — clears the contact in the same
// statement that marks it withdrawn.
func TestAMarketingConsentIsStoredAndWithdrawn(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	version := publishConsent(t, w)
	repo := w.app.GuestData()

	if _, err := repo.RecordConsent(ctx, store.ConsentGiven{SiteID: w.site.ID, ClientMAC: "02:00:00:00:00:41",
		Email: strp("x@example.test"), Version: store.ConsentVersion("a text nobody published")}); !errors.Is(err, store.ErrUnknownConsentVersion) {
		t.Fatalf("a consent to an unpublished text was stored: %v", err)
	}
	if n := w.count(t, `SELECT count(*) FROM marketing_consents`); n != 0 {
		t.Fatalf("%d consent row(s) after a refused consent", n)
	}

	c, err := repo.RecordConsent(ctx, store.ConsentGiven{SiteID: w.site.ID, ClientMAC: "02:00:00:00:00:41",
		Name: strp("Lan"), Email: strp("lan@example.test"), Version: version})
	fatalf(t, err, "recording the consent")
	got, err := repo.Consent(ctx, w.site.ID, c.ID)
	fatalf(t, err, "reading it back")
	if got.Text != consentText || got.Lang != "vi" || got.Version != version || got.WithdrawnAt != nil {
		t.Fatalf("the stored consent does not say what the guest read: %+v", got)
	}

	// The guest's own withdrawal, from the portal, by device.
	n, err := repo.WithdrawDevice(ctx, w.site.ID, "02-00-00-00-00-41", "guest")
	fatalf(t, err, "withdrawing")
	if n != 1 {
		t.Fatalf("withdrew %d, want 1", n)
	}
	got, err = repo.Consent(ctx, w.site.ID, c.ID)
	fatalf(t, err, "reading it back")
	if got.WithdrawnAt == nil || got.WithdrawnBy == nil || *got.WithdrawnBy != "guest" {
		t.Fatalf("not withdrawn: %+v", got)
	}
	if got.Name != nil || got.Email != nil || got.Phone != nil {
		t.Errorf("a withdrawn consent still holds the contact: %+v", got)
	}
	if got.Version != version || !got.ConsentedAt.Before(time.Now().Add(time.Minute)) {
		t.Error("withdrawal lost the evidence of when and what")
	}

	// An operator withdrawing again changes nothing: idempotent, and still the guest's.
	again, err := repo.Withdraw(ctx, w.site.ID, c.ID, "operator")
	fatalf(t, err, "withdrawing again")
	if *again.WithdrawnBy != "guest" || !again.WithdrawnAt.Equal(*got.WithdrawnAt) {
		t.Errorf("a second withdrawal rewrote the first: %+v", again)
	}

	// The schema, not only the code, refuses a withdrawn row with a contact on it.
	if _, err := w.owner.Exec(ctx, `UPDATE marketing_consents SET email = 'back@example.test' WHERE id = $1`, c.ID); err == nil {
		t.Error("the schema let a withdrawn consent regain an e-mail address")
	}
	page, err := repo.Consents(ctx, store.ConsentFilter{ListOptions: store.ListOptions{SiteID: w.site.ID}, State: "active"})
	fatalf(t, err, "listing")
	if len(page.Items) != 0 {
		t.Errorf("a withdrawn consent is listed as active: %+v", page.Items)
	}
}

// TestAGuestWhoDidNotConsentHasNoMarketingRow: the details a guest typed are kept
// with their grant, and nothing about them reaches the consent table or its export.
func TestAGuestWhoDidNotConsentHasNoMarketingRow(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	publishConsent(t, w)
	now := time.Now().UTC()
	g := w.grant(t, "02:00:00:00:00:42", nil, now, nil)
	fatalf(t, w.app.GuestData().RecordDetails(ctx, store.GuestDetails{GrantID: g, SiteID: w.site.ID,
		Email: strp("minh@example.test")}), "recording details")

	if n := w.count(t, `SELECT count(*) FROM guest_details WHERE grant_id = $1`, g); n != 1 {
		t.Fatalf("the details were not kept with the grant")
	}
	if n := w.count(t, `SELECT count(*) FROM marketing_consents`); n != 0 {
		t.Fatalf("a guest who did not consent has %d marketing row(s)", n)
	}
	var rows int
	fatalf(t, w.app.GuestData().ConsentsBetween(ctx, w.site.ID, now.Add(-time.Hour), now.Add(time.Hour),
		func(store.MarketingConsent) error { rows++; return nil }), "exporting")
	if rows != 0 {
		t.Fatalf("the export carries %d row(s) for a guest who did not consent", rows)
	}
}

// TestEraseByMACRemovesGuestDetailsAndConsents is data-subject erasure reaching
// the guest-data tables: the consent by its own MAC, the details through their grant.
func TestEraseByMACRemovesGuestDetailsAndConsents(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	version := publishConsent(t, w)
	now := time.Now().UTC()
	const mac, bystander = "02:00:00:00:00:43", "02:00:00:00:00:44"
	for _, m := range []string{mac, bystander} {
		g := w.grant(t, m, nil, now, nil)
		fatalf(t, w.app.GuestData().RecordDetails(ctx, store.GuestDetails{GrantID: g, SiteID: w.site.ID,
			Email: strp(m + "@example.test")}), "details")
		_, err := w.app.GuestData().RecordConsent(ctx, store.ConsentGiven{SiteID: w.site.ID, ClientMAC: m,
			GrantID: &g, Email: strp(m + "@example.test"), Version: version})
		fatalf(t, err, "consent")
	}

	s, err := store.MACSubject(strings.ToUpper(strings.ReplaceAll(mac, ":", "-")))
	fatalf(t, err, "subject")
	rep, err := store.Erase(ctx, w.app, w.ret, s)
	fatalf(t, err, "erasing")

	if n := w.count(t, `SELECT count(*) FROM marketing_consents WHERE client_mac = $1::macaddr`, mac); n != 0 {
		t.Errorf("the erased device's consent survived")
	}
	if n := w.count(t, `SELECT count(*) FROM guest_details WHERE email = $1`, mac+"@example.test"); n != 0 {
		t.Errorf("the erased device's details survived")
	}
	if n := w.count(t, `SELECT count(*) FROM marketing_consents`); n != 1 {
		t.Errorf("the bystander's consent went too: %d left", n)
	}
	if n := w.count(t, `SELECT count(*) FROM guest_details`); n != 1 {
		t.Errorf("the bystander's details went too: %d left", n)
	}
	found := false
	for _, r := range rep.Removed {
		if r.Table == "marketing_consents" && r.Rows == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("the receipt does not report the consent it removed: %+v", rep.Removed)
	}
}

// TestTheRetentionSweepAgesConsentsOut: past its period a consent — active by when
// it was given, withdrawn by when it was withdrawn — is gone after one sweep.
func TestTheRetentionSweepAgesConsentsOut(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	version := publishConsent(t, w)
	old := func(mac string) string {
		c, err := w.app.GuestData().RecordConsent(ctx, store.ConsentGiven{SiteID: w.site.ID, ClientMAC: mac,
			Email: strp("a@example.test"), Version: version})
		fatalf(t, err, "consent")
		return c.ID
	}
	stale, fresh := old("02:00:00:00:00:45"), old("02:00:00:00:00:46")
	w.exec(t, `UPDATE marketing_consents SET consented_at = now() - interval '800 days' WHERE id = $1`, stale)

	_, err := store.SweepRetention(ctx, store.SweepTargets{App: w.app, Retention: w.ret}, time.Now().UTC(), discard())
	fatalf(t, err, "sweeping")
	if n := w.count(t, `SELECT count(*) FROM marketing_consents WHERE id = $1`, stale); n != 0 {
		t.Error("an 800-day-old consent survived a 730-day period")
	}
	if n := w.count(t, `SELECT count(*) FROM marketing_consents WHERE id = $1`, fresh); n != 1 {
		t.Error("a fresh consent was swept")
	}
}
