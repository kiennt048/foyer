package otp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// Each test here pins a limit the package comment and the contract state, and
// which no other test would notice losing to a single-mechanism sabotage.

// sendFrom sends one code to phone from a device and address nobody else uses,
// so only the per-number limit can refuse it.
func (r *rig) sendFrom(i int, phone string) error {
	mac := fmt.Sprintf("02:00:00:00:%02x:%02x", i/256, i%256)
	ip := fmt.Sprintf("10.1.%d.%d", i/256, i%256)
	return r.svc.Send(context.Background(), req(fmt.Sprintf("sess-%d", i), mac, ip, phone))
}

// Five a day per number, and not only three an hour. Without this test, removing
// `day >= PerNumberDay` from either store leaves every test green: the day limit is
// stated in the contract and in security.md §9, and would be asserted nowhere.
func TestPerNumberDailyLimit(t *testing.T) {
	r := newRig(t)
	const phone = "0912345678"
	n := 0
	// Three in the first hour, then the hour limit refuses a fourth.
	for i := 0; i < 3; i++ {
		if err := r.sendFrom(n, phone); err != nil {
			t.Fatalf("send %d in hour one: %v", i, err)
		}
		n++
	}
	if err := r.sendFrom(n, phone); !errors.Is(err, ErrLimited) {
		t.Fatalf("fourth in one hour: %v", err)
	}
	n++
	// An hour later the hour window has room again: two more fit (5 today).
	r.advance(time.Hour + time.Minute)
	for i := 0; i < 2; i++ {
		if err := r.sendFrom(n, phone); err != nil {
			t.Fatalf("send %d in hour two: %v", i, err)
		}
		n++
	}
	// The sixth of the day is refused although the hour window has room for it.
	if err := r.sendFrom(n, phone); !errors.Is(err, ErrLimited) {
		t.Fatalf("sixth code to one number in a day: %v", err)
	}
	n++
	if got := len(r.sms.Sent()); got != DefaultLimits.PerNumberDay {
		t.Fatalf("%d messages to one number in a day, want %d", got, DefaultLimits.PerNumberDay)
	}
	// And the day is a rolling 24 hours, not a lockout.
	r.advance(24 * time.Hour)
	if err := r.sendFrom(n, phone); err != nil {
		t.Fatalf("a day later: %v", err)
	}
}

// The lifetime is five minutes as a number, not "whatever DefaultLimits says":
// TestACodeExpiresAfterFiveMinutes advances by DefaultLimits.CodeLifetime, so it
// stays green if the lifetime is changed to fifty minutes.
func TestTheCodeLifetimeIsFiveMinutes(t *testing.T) {
	if DefaultLimits.CodeLifetime != 5*time.Minute {
		t.Fatalf("code lifetime %v; the contract, the portal copy and NIST 800-63B §5.1.3 all say 5 minutes", DefaultLimits.CodeLifetime)
	}
	r := newRig(t)
	ctx := context.Background()
	_ = r.svc.Send(ctx, req("sess1", "aa:bb:cc:dd:ee:01", "10.0.0.1", "0912345678"))
	code := r.sms.Last().Code
	r.advance(4*time.Minute + 59*time.Second)
	if _, err := r.svc.Check(ctx, "sess1", code); err != nil {
		t.Fatalf("a code 4m59s old was refused: %v", err)
	}
	_ = r.svc.Send(ctx, req("sess2", "aa:bb:cc:dd:ee:02", "10.0.0.2", "0987654321"))
	code = r.sms.Last().Code
	r.advance(5 * time.Minute)
	if _, err := r.svc.Check(ctx, "sess2", code); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("a code 5m old was accepted: %v", err)
	}
}

// The MAC binds the code to the browser as well as to the row (code.go: "a MAC
// copied onto another row or another session does not verify"). Removing the
// session hash from the MAC input left every test green, because the store's
// lookup by session hid it.
func TestTheMACDependsOnEveryInput(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	base := MAC(key, "otp_1", "sess1", "123456")
	for name, other := range map[string][]byte{
		"code id": MAC(key, "otp_2", "sess1", "123456"),
		"session": MAC(key, "otp_1", "sess2", "123456"),
		"code":    MAC(key, "otp_1", "sess1", "123457"),
		"key":     MAC([]byte("another key entirely, 32 bytes!!"), "otp_1", "sess1", "123456"),
		// Field boundaries are delimited: moving a character between two inputs
		// is a different MAC.
		"boundary": MAC(key, "otp_1s", "ess1", "123456"),
	} {
		if bytes.Equal(base, other) {
			t.Errorf("the MAC does not depend on the %s", name)
		}
	}
}
