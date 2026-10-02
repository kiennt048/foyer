package store_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

func phoneConfig(siteID string) *store.PhoneSignIn {
	b := "FOYER"
	return &store.PhoneSignIn{
		SiteID: siteID, Provider: "esms-sms", Brandname: &b,
		DailyLimit: 3, SessionMinutes: 60, MessageTemplate: "{code} la ma Wi-Fi",
	}
}

func issue(site, session, phone string, at time.Time, n int) store.PhoneCodeIssue {
	return store.PhoneCodeIssue{
		ID: uniq("otp"), SiteID: site, SessionTokenHash: session, ClientMAC: "aa:bb:cc:00:00:01",
		Phone: phone, CodeMAC: bytes.Repeat([]byte{byte(n)}, 32), KeyID: "k1",
		At: at, ExpiresAt: at.Add(5 * time.Minute), MaxAttempts: 5,
		Day: at.Format("2006-01-02"), DailyLimit: 3, PerNumberHour: 3, PerNumberDay: 5,
	}
}

func TestPhoneProviderKeysAreSealedAndNeverRead(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	if err := st.Phone().Upsert(ctx, phoneConfig(site.ID), nil, nil); !errors.Is(err, store.ErrSecretUnset) {
		t.Fatalf("first write without keys: %v", err)
	}
	fatalf(t, st.Phone().Upsert(ctx, phoneConfig(site.ID), []byte("API-KEY-VALUE"), []byte("SECRET-KEY-VALUE")), "upsert")

	var rawA, rawS []byte
	fatalf(t, ownerPool(t).QueryRow(ctx, `SELECT api_key, secret_key FROM phone_sign_in WHERE site_id = $1`, site.ID).
		Scan(&rawA, &rawS), "raw read")
	if bytes.Contains(rawA, []byte("API-KEY-VALUE")) || bytes.Contains(rawS, []byte("SECRET-KEY-VALUE")) {
		t.Fatal("a provider key is at rest in the clear")
	}
	a, s, err := st.Phone().RevealKeys(ctx, site.ID)
	if err != nil || string(a) != "API-KEY-VALUE" || string(s) != "SECRET-KEY-VALUE" {
		t.Fatalf("reveal: %q %q %v", a, s, err)
	}
	// Omitted keys are unchanged.
	fatalf(t, st.Phone().Upsert(ctx, phoneConfig(site.ID), nil, nil), "second upsert")
	if a, _, _ := st.Phone().RevealKeys(ctx, site.ID); string(a) != "API-KEY-VALUE" {
		t.Fatalf("an update without keys changed them: %q", a)
	}

	// FreeRADIUS's role has no business with any of this.
	radius := storeAsRole(t, "foyer_radius", store.DefaultTenantID)
	_, err = radius.Pool().Exec(ctx, `SELECT api_key FROM phone_sign_in`)
	if err == nil || !strings.Contains(err.Error(), "phone_sign_in") {
		t.Fatalf("foyer_radius read phone_sign_in: %v", err)
	}
	_, err = radius.Pool().Exec(ctx, `SELECT code_mac FROM phone_codes`)
	if err == nil || !strings.Contains(err.Error(), "phone_codes") {
		t.Fatalf("foyer_radius read phone_codes: %v", err)
	}
}

func TestPhoneCeilingHoldsUnderConcurrency(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	at := time.Now().UTC().Truncate(time.Second)

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, ceiling := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// A distinct number each, so only the ceiling can refuse.
			phone := "+8491234560" + string(rune('0'+i%10)) + string(rune('0'+i/10))
			err := st.Phone().Issue(ctx, issue(site.ID, uniq("sess"), phone, at, i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, store.ErrPhoneCeiling):
				ceiling++
			default:
				t.Errorf("issue %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if ok != 3 || ceiling != 9 {
		t.Fatalf("%d issued and %d refused against a ceiling of 3", ok, ceiling)
	}
	if n, _ := st.Phone().SentOn(ctx, site.ID, at.Format("2006-01-02")); n != 3 {
		t.Fatalf("sent today = %d", n)
	}
}

func TestPhonePerNumberLimitHoldsUnderConcurrency(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	at := time.Now().UTC().Truncate(time.Second)

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := issue(site.ID, uniq("sess"), "+84987654321", at, i)
			in.DailyLimit = 1000
			err := st.Phone().Issue(ctx, in)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, store.ErrPhoneLimited):
				limited++
			default:
				t.Errorf("issue %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if ok != 3 || limited != 5 {
		t.Fatalf("%d codes to one number and %d refused, against a limit of 3 an hour", ok, limited)
	}
}

func TestPhoneAttemptsAreSpentBeforeTheMACIsRead(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	at := time.Now().UTC().Truncate(time.Second)
	sess := uniq("sess")
	in := issue(site.ID, sess, "+84912000111", at, 7)
	fatalf(t, st.Phone().Issue(ctx, in), "issue")

	var wg sync.WaitGroup
	var mu sync.Mutex
	got := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.Phone().Attempt(ctx, sess, at.Add(time.Second)); err == nil {
				mu.Lock()
				got++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if got != 5 {
		t.Fatalf("%d attempts were granted a comparison against a bound of 5", got)
	}
	// Expired codes are not live, and single use holds.
	sess2 := uniq("sess")
	in2 := issue(site.ID, sess2, "+84912000222", at, 8)
	fatalf(t, st.Phone().Issue(ctx, in2), "issue 2")
	if _, err := st.Phone().Attempt(ctx, sess2, at.Add(5*time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an expired code was live: %v", err)
	}
	a, err := st.Phone().Attempt(ctx, sess2, at.Add(time.Second))
	fatalf(t, err, "attempt")
	if !bytes.Equal(a.CodeMAC, in2.CodeMAC) || a.Phone != "+84912000222" {
		t.Fatalf("attempt returned %+v", a)
	}
	fatalf(t, st.Phone().Consume(ctx, a.ID, at), "consume")
	if err := st.Phone().Consume(ctx, a.ID, at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("consumed twice: %v", err)
	}
}

func TestPhoneNewCodeRetiresTheOldOne(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	at := time.Now().UTC().Truncate(time.Second)
	sess := uniq("sess")
	first := issue(site.ID, sess, "+84912000333", at, 1)
	fatalf(t, st.Phone().Issue(ctx, first), "first")
	second := issue(site.ID, sess, "+84912000333", at.Add(time.Second), 2)
	fatalf(t, st.Phone().Issue(ctx, second), "second")
	a, err := st.Phone().Attempt(ctx, sess, at.Add(2*time.Second))
	fatalf(t, err, "attempt")
	if a.ID != second.ID {
		t.Fatalf("the live code is %s, want the newest %s", a.ID, second.ID)
	}
	if err := st.Phone().Consume(ctx, first.ID, at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the retired code could still be consumed: %v", err)
	}
}

func TestPhoneGrantCarriesTheNumber(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	phone := "+84912000444"
	g := &store.PortalGrant{ID: uniq("grant"), SiteID: site.ID, ClientMAC: "aa:bb:cc:00:00:02",
		Method: "phone", Phone: &phone, GrantedAt: time.Now()}
	fatalf(t, st.Grants().CreateGrant(ctx, g), "create")
	back, err := st.Grants().Grant(ctx, g.ID)
	fatalf(t, err, "read")
	if back.Phone == nil || *back.Phone != phone || back.Method != "phone" {
		t.Fatalf("grant %+v", back)
	}
	// The method and the number are held together by the schema.
	bad := &store.PortalGrant{ID: uniq("grant"), SiteID: site.ID, ClientMAC: "aa:bb:cc:00:00:03",
		Method: "phone", GrantedAt: time.Now()}
	if err := st.Grants().CreateGrant(ctx, bad); err == nil {
		t.Fatal("a phone grant with no number was stored")
	}
}
