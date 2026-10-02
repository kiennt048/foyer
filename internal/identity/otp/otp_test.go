package otp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/identity/sms"
)

const site = "site_a"

type rig struct {
	svc   *Service
	store *Memory
	sms   *sms.Fake
	clock *time.Time
	logs  *bytes.Buffer
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st := NewMemory()
	st.Configure(site, Settings{
		Provider: sms.KindESMSSMS, Brandname: "FOYER", APIKey: "k", SecretKey: "s",
		DailyLimit: 100, SessionMinutes: 60, MessageTemplate: "{code} la ma dang nhap Wi-Fi",
	})
	fake := &sms.Fake{}
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	logs := &bytes.Buffer{}
	svc, err := New(Config{
		Store: st, Keys: StaticKeys{ID: "k1", Key: []byte("0123456789abcdef0123456789abcdef")},
		Provider: func(sms.Config) (sms.Provider, error) { return fake, nil },
		Now:      func() time.Time { return now },
		Location: time.UTC,
		Logger:   slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{svc: svc, store: st, sms: fake, clock: &now, logs: logs}
}

func (r *rig) advance(d time.Duration) { *r.clock = r.clock.Add(d) }

func req(session, mac, ip, phone string) Request {
	return Request{SiteID: site, SessionTokenHash: session, ClientMAC: mac, ClientIP: ip, Phone: phone}
}

func TestPhoneSignInHappyPathWithTheFakeProvider(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.svc.Send(ctx, req("sess1", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912 345 678")); err != nil {
		t.Fatalf("send: %v", err)
	}
	m := r.sms.Last()
	if m.To != "+84912345678" {
		t.Fatalf("sent to %q", m.To)
	}
	if !strings.HasPrefix(m.Text, m.Code+" ") || len(m.Code) != 6 {
		t.Fatalf("text %q code %q", m.Text, m.Code)
	}
	phone, err := r.svc.Check(ctx, "sess1", m.Code)
	if err != nil || phone != "+84912345678" {
		t.Fatalf("check: %q %v", phone, err)
	}
	// Single use.
	if _, err := r.svc.Check(ctx, "sess1", m.Code); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("second use: %v", err)
	}
}

func TestACodeIsBoundToTheBrowserItWasSentFor(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	_ = r.svc.Send(ctx, req("sess1", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912345678"))
	if _, err := r.svc.Check(ctx, "sess2", r.sms.Last().Code); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("another browser used the code: %v", err)
	}
}

func TestTheCodeIsHashedAtRestUnderAKey(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	_ = r.svc.Send(ctx, req("sess1", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912345678"))
	m := r.sms.Last()
	row, ok := r.store.Stored(m.RequestID)
	if !ok {
		t.Fatal("no stored row")
	}
	if bytes.Contains(row.MAC, []byte(m.Code)) {
		t.Fatal("the code is stored in the clear")
	}
	plain := sha256.Sum256([]byte(m.Code))
	if bytes.Equal(row.MAC, plain[:]) {
		t.Fatal("the code is stored under an unkeyed hash, which a million guesses invert")
	}
	want := MAC([]byte("0123456789abcdef0123456789abcdef"), row.ID, "sess1", m.Code)
	if !bytes.Equal(row.MAC, want) || row.KeyID != "k1" {
		t.Fatal("the stored MAC is not the keyed MAC of this row, session and code")
	}
	other := MAC([]byte("another key entirely, 32 bytes!!"), row.ID, "sess1", m.Code)
	if bytes.Equal(row.MAC, other) {
		t.Fatal("the MAC does not depend on the key")
	}
}

func TestACodeExpiresAfterFiveMinutes(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	_ = r.svc.Send(ctx, req("sess1", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912345678"))
	r.advance(DefaultLimits.CodeLifetime)
	if _, err := r.svc.Check(ctx, "sess1", r.sms.Last().Code); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("an expired code was accepted: %v", err)
	}
}

func TestWrongAttemptsAreBoundedPerCode(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	_ = r.svc.Send(ctx, req("sess1", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912345678"))
	right := r.sms.Last().Code
	wrong := "000000"
	if wrong == right {
		wrong = "111111"
	}
	for i := 1; i < DefaultLimits.MaxAttempts; i++ {
		if _, err := r.svc.Check(ctx, "sess1", wrong); !errors.Is(err, ErrCodeWrong) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := r.svc.Check(ctx, "sess1", wrong); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("the last attempt should kill the code: %v", err)
	}
	if _, err := r.svc.Check(ctx, "sess1", right); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("the right code was accepted after the attempts ran out: %v", err)
	}
}

func TestANonNumericSubmissionSpendsNoAttempt(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	_ = r.svc.Send(ctx, req("sess1", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912345678"))
	for i := 0; i < 10; i++ {
		if _, err := r.svc.Check(ctx, "sess1", "12ab56"); !errors.Is(err, ErrCodeWrong) {
			t.Fatal(err)
		}
	}
	if _, err := r.svc.Check(ctx, "sess1", r.sms.Last().Code); err != nil {
		t.Fatalf("malformed input spent attempts: %v", err)
	}
}

func TestANewCodeRetiresTheOldOne(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	_ = r.svc.Send(ctx, req("sess1", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912345678"))
	first := r.sms.Last().Code
	r.advance(time.Second)
	_ = r.svc.Send(ctx, req("sess1", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912345678"))
	second := r.sms.Last().Code
	if first != second {
		if _, err := r.svc.Check(ctx, "sess1", first); err == nil {
			t.Fatal("the retired code still works")
		}
	}
	if _, err := r.svc.Check(ctx, "sess1", second); err != nil {
		t.Fatalf("the newest code: %v", err)
	}
}

func TestPerNumberLimit(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for i := 0; i < DefaultLimits.PerNumberHour; i++ {
		// A different device and address each time, so only the number limit applies.
		mac := "aa:bb:cc:dd:ee:0" + string(rune('1'+i))
		if err := r.svc.Send(ctx, req("s", mac, "10.0.0."+string(rune('1'+i)), "0912345678")); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if err := r.svc.Send(ctx, req("s", "aa:bb:cc:dd:ee:09", "10.0.0.9", "+84 912 345 678")); !errors.Is(err, ErrLimited) {
		t.Fatalf("fourth code to one number in an hour: %v", err)
	}
	if n := len(r.sms.Sent()); n != DefaultLimits.PerNumberHour {
		t.Fatalf("%d messages sent", n)
	}
	// And it recovers: a limit that never refills is a lockout.
	r.advance(time.Hour)
	if err := r.svc.Send(ctx, req("s", "aa:bb:cc:dd:ee:09", "10.0.0.9", "0912345678")); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
}

func TestPerDeviceLimit(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	// One device on one source, and a fresh session each time: deleting the cookie
	// and arriving again must not be a fresh budget (see DeviceKey).
	for i := 0; i < DefaultLimits.PerDeviceHour; i++ {
		phone := "091234567" + string(rune('0'+i))
		if err := r.svc.Send(ctx, req("s"+string(rune('a'+i)), "aa:bb:cc:dd:ee:01", "10.0.0.1", phone)); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if err := r.svc.Send(ctx, req("s-new", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0987654321")); !errors.Is(err, ErrLimited) {
		t.Fatalf("one device, six numbers: %v", err)
	}
}

// TestASpoofedMACDoesNotSpendTheGuestsDeviceLimit: presence proves a MAC is on
// the network, not that the caller is the
// device: a stranger who claims a present guest's MAC from their own laptop must
// spend their own budget, and the guest — from their own connection — still gets
// a code.
func TestASpoofedMACDoesNotSpendTheGuestsDeviceLimit(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	const guestMAC = "aa:bb:cc:dd:ee:42"
	// The stranger exhausts "the device" from 10.0.0.66 — five codes to five
	// numbers, each on a fresh session.
	for i := 0; i < DefaultLimits.PerDeviceHour; i++ {
		if err := r.svc.Send(ctx, req(fmt.Sprintf("spoof-%d", i), guestMAC, "10.0.0.66", "09"+pad(20+i, 8))); err != nil {
			t.Fatalf("stranger's send %d: %v", i, err)
		}
	}
	if err := r.svc.Send(ctx, req("spoof-x", guestMAC, "10.0.0.66", "0987650000")); !errors.Is(err, ErrLimited) {
		t.Fatalf("the stranger's own device bucket did not bind: %v", err)
	}
	// The real guest, on the address the connection actually came from.
	if err := r.svc.Send(ctx, req("guest", guestMAC, "10.0.0.42", "0912345678")); err != nil {
		t.Fatalf("a stranger claiming the guest's MAC spent the guest's limit: %v", err)
	}
}

func TestPerAddressLimit(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for i := 0; i < DefaultLimits.PerAddressHour; i++ {
		mac := "aa:bb:cc:dd:" + hex2(i/256) + ":" + hex2(i%256)
		phone := "09" + pad(i, 8)
		if err := r.svc.Send(ctx, req("s", mac, "10.0.0.1", phone)); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if err := r.svc.Send(ctx, req("s", "aa:bb:cc:dd:ff:ff", "10.0.0.1", "0987654321")); !errors.Is(err, ErrLimited) {
		t.Fatalf("one address past its limit: %v", err)
	}
}

func TestTheDailyCeilingStopsSendingAndResetsTomorrow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.store.Configure(site, Settings{Provider: sms.KindESMSSMS, DailyLimit: 2, SessionMinutes: 60, MessageTemplate: "{code}"})
	for i := 0; i < 2; i++ {
		if err := r.svc.Send(ctx, req("s", "aa:bb:cc:dd:ee:0"+string(rune('1'+i)), "10.0.0."+string(rune('1'+i)), "091234567"+string(rune('0'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.svc.Send(ctx, req("s", "aa:bb:cc:dd:ee:09", "10.0.0.9", "0987654321")); !errors.Is(err, ErrCeiling) {
		t.Fatalf("third message against a ceiling of two: %v", err)
	}
	if n := len(r.sms.Sent()); n != 2 {
		t.Fatalf("%d messages went out against a ceiling of two", n)
	}
	r.advance(24 * time.Hour)
	if err := r.svc.Send(ctx, req("s", "aa:bb:cc:dd:ee:09", "10.0.0.9", "0987654321")); err != nil {
		t.Fatalf("the next day: %v", err)
	}
}

func TestAnUnreadableCeilingFailsClosed(t *testing.T) {
	r := newRig(t)
	r.store.FailCeiling = true
	if err := r.svc.Send(context.Background(), req("s", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912345678")); !errors.Is(err, ErrCeiling) {
		t.Fatalf("err %v", err)
	}
	if len(r.sms.Sent()) != 0 {
		t.Fatal("a message was sent past a ceiling nobody could read")
	}
}

func TestAFailedSendRetiresTheCode(t *testing.T) {
	r := newRig(t)
	r.sms.Fail = sms.ErrRefused
	ctx := context.Background()
	if err := r.svc.Send(ctx, req("s1", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912345678")); !errors.Is(err, ErrSendFailed) {
		t.Fatalf("err %v", err)
	}
	// Brute-forcing a code that never arrived must be impossible, not just slow.
	for c := 0; c < 3; c++ {
		if _, err := r.svc.Check(ctx, "s1", "00000"+string(rune('0'+c))); !errors.Is(err, ErrCodeExpired) {
			t.Fatalf("retired code accepted an attempt: %v", err)
		}
	}
}

func TestNotOfferedWithoutSettings(t *testing.T) {
	r := newRig(t)
	if err := r.svc.Send(context.Background(), Request{SiteID: "elsewhere", Phone: "0912345678"}); !errors.Is(err, ErrNotOffered) {
		t.Fatalf("err %v", err)
	}
	if _, ok := r.svc.Offered(context.Background(), "elsewhere"); ok {
		t.Fatal("offered")
	}
	if m, ok := r.svc.Offered(context.Background(), site); !ok || m != 60 {
		t.Fatalf("offered %d %v", m, ok)
	}
}

func TestNeitherTheNumberNorTheCodeIsLogged(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	_ = r.svc.Send(ctx, req("s1", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912345678"))
	code := r.sms.Last().Code
	_, _ = r.svc.Check(ctx, "s1", "000000")
	r.sms.Fail = sms.ErrRefused
	_ = r.svc.Send(ctx, req("s1", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912345678"))
	out := r.logs.String()
	if out == "" {
		t.Fatal("nothing was logged at all, so this test proves nothing")
	}
	for _, leak := range []string{"912345678", code, "\"k\"", "secret"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log carries %q:\n%s", leak, out)
		}
	}
}

func TestNormaliseVN(t *testing.T) {
	ok := map[string]string{
		"0912345678":          "+84912345678",
		"0912 345 678":        "+84912345678",
		"091.234.5678":        "+84912345678",
		"+84912345678":        "+84912345678",
		"84912345678":         "+84912345678",
		"0084912345678":       "+84912345678",
		"+84 (0) 912 345 678": "+84912345678",
		"0321234567":          "+84321234567",
		"0561234567":          "+84561234567",
		"0701234567":          "+84701234567",
		"0861234567":          "+84861234567",
	}
	for in, want := range ok {
		got, err := NormaliseVN(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "0241234567", // Hà Nội landline
		"+12025550123", "+882123456789", // another country; an international premium range
		"091234567", "09123456789", "+8491234567", "0912345678x", "1900 1234",
		"0112345678", "+84 0 0 912345678",
	} {
		if got, err := NormaliseVN(bad); err == nil {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
}

func TestNewCodeIsSixDigitsAndVaries(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := NewCode()
		if err != nil || len(c) != 6 || NormaliseCode(c) != c {
			t.Fatalf("code %q %v", c, err)
		}
		seen[c] = true
	}
	if len(seen) < 190 {
		t.Fatalf("only %d distinct codes in 200", len(seen))
	}
}

func hex2(n int) string {
	const h = "0123456789abcdef"
	return string([]byte{h[n>>4&15], h[n&15]})
}

func pad(n, width int) string {
	s := ""
	for i := 0; i < width; i++ {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
