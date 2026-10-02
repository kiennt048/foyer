package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
)

// The attacker lockout: without a device cookie, five wrong passwords from any
// device, then two a minute, keep an account answering 429 for as long as the
// attacker cares to — the owner included, because the per-account bucket does not
// know who is asking. OWASP's device-cookie defence splits it.

func (r *throttleRig) attemptWith(username, password string, cookies ...*http.Cookie) *http.Response {
	r.t.Helper()
	saved := r.cookie
	r.cookie = nil
	defer func() { r.cookie = saved }()
	req, err := http.NewRequest(http.MethodPost, r.server.URL+"/api/v0/auth/session",
		strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := r.server.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	return resp
}

func deviceCookieOf(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-foyer_device" {
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" {
				t.Fatalf("the device cookie is not HttpOnly/Secure/Strict/host-only: %+v", c)
			}
			return c
		}
	}
	t.Fatal("a successful sign-in set no trusted-device cookie")
	return nil
}

func TestAStrangerCannotLockTheOwnerOutOfTheirAccount(t *testing.T) {
	r := newThrottleRig(t)

	// The owner signed in from this browser once, last week.
	first := r.attemptWith(adminUser, testSecret)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("the owner's first sign-in answered %d", first.StatusCode)
	}
	device := deviceCookieOf(t, first)

	// Somebody else on the network empties the account's bucket.
	for i := 0; i < 5; i++ {
		r.attemptWith(adminUser, "a-guess").Body.Close()
	}
	assertRateLimited(t, r.attemptWith(adminUser, testSecret))

	// The owner, from the browser they used before, is not refused.
	back := r.attemptWith(adminUser, testSecret, device)
	back.Body.Close()
	if back.StatusCode != http.StatusOK {
		t.Fatalf("the owner at a trusted device answered %d while a stranger held the account's bucket empty, want 200", back.StatusCode)
	}
	// ...and signing in did not refund the stranger: the shared bucket is still empty.
	assertRateLimited(t, r.attemptWith(adminUser, "another-guess"))
}

func TestADeviceCookieIsTrustedOnlyForItsAccountAndOnlyIfSigned(t *testing.T) {
	r := newThrottleRig(t)
	first := r.attemptWith(staffUser, testSecret)
	first.Body.Close()
	staffDevice := deviceCookieOf(t, first)

	for i := 0; i < 5; i++ {
		r.attemptWith(adminUser, "a-guess").Body.Close()
	}
	// Another account's cookie does not open this account's own bucket.
	assertRateLimited(t, r.attemptWith(adminUser, testSecret, staffDevice))

	// Nor does a cookie with its signature altered. A fresh rig, so the address
	// bucket (ten) cannot be what refuses.
	r = newThrottleRig(t)
	first = r.attemptWith(staffUser, testSecret)
	first.Body.Close()
	forged := *deviceCookieOf(t, first)
	tail := "AA"
	if strings.HasSuffix(forged.Value, tail) {
		tail = "BB"
	}
	forged.Value = forged.Value[:len(forged.Value)-2] + tail
	for i := 0; i < 5; i++ {
		r.attemptWith(staffUser, "a-guess").Body.Close()
	}
	assertRateLimited(t, r.attemptWith(staffUser, testSecret, &forged))
}

// TestATrustedDeviceIsStillThrottled: trusted to be the owner, not to guess faster.
func TestATrustedDeviceIsStillThrottled(t *testing.T) {
	r := newThrottleRig(t)
	first := r.attemptWith(adminUser, testSecret)
	first.Body.Close()
	device := deviceCookieOf(t, first)
	for i := 0; i < 5; i++ {
		resp := r.attemptWith(adminUser, "a-guess", device)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d from the trusted device answered %d, want 401", i+1, resp.StatusCode)
		}
	}
	assertRateLimited(t, r.attemptWith(adminUser, testSecret, device))
}

// restart replaces the rig's server with a new one over the same store and clock:
// a process restart or an upgrade, with the database surviving it.
func (r *throttleRig) restart() {
	r.t.Helper()
	srv, err := api.New(api.Config{
		Ports: r.store.Ports(),
		Now:   func() time.Time { return at(0).Add(time.Duration(r.clock.Load()) * time.Second) },
	})
	if err != nil {
		r.t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	r.t.Cleanup(ts.Close)
	r.server = ts
}

// TestATrustedDeviceSurvivesARestart: a key drawn when the process starts would
// untrust every device at once on a restart or an upgrade — and a stranger already
// emptying the shared bucket would then lock the owner out. The ring is persisted
// through `Ports.DeviceKeys`.
func TestATrustedDeviceSurvivesARestart(t *testing.T) {
	r := newThrottleRig(t)
	first := r.attemptWith(adminUser, testSecret)
	first.Body.Close()
	device := deviceCookieOf(t, first)

	r.restart()

	for i := 0; i < 5; i++ {
		r.attemptWith(adminUser, "a-guess").Body.Close()
	}
	assertRateLimited(t, r.attemptWith(adminUser, testSecret))

	back := r.attemptWith(adminUser, testSecret, device)
	back.Body.Close()
	if back.StatusCode != http.StatusOK {
		t.Fatalf("after a restart the owner's trusted device answered %d while a stranger held the shared bucket empty, want 200 — the device key did not survive the restart", back.StatusCode)
	}
}

// TestTheDeviceKeyRotatesOnPolicyAndARetiredKeyStillVerifies: the key changes by a
// deliberate policy (ninety days), never by a restart, and a cookie signed by the
// retired key is honoured for a cookie's lifetime after its successor arrived —
// then no longer.
func TestTheDeviceKeyRotatesOnPolicyAndARetiredKeyStillVerifies(t *testing.T) {
	r := newThrottleRig(t)
	first := r.attemptWith(adminUser, testSecret)
	first.Body.Close()
	old := deviceCookieOf(t, first)
	keyOf := func(c *http.Cookie) string {
		parts := strings.Split(c.Value, ".")
		if len(parts) != 5 {
			t.Fatalf("device cookie %q is not v2.<key>.<account>.<nonce>.<mac>", c.Value)
		}
		return parts[1]
	}

	// A restart the next day keeps the key.
	r.clock.Store(24 * 3600)
	r.restart()
	again := r.attemptWith(adminUser, testSecret)
	again.Body.Close()
	if got, want := keyOf(deviceCookieOf(t, again)), keyOf(old); got != want {
		t.Fatalf("a restart changed the signing key (%s -> %s)", want, got)
	}

	// Ninety-one days on, a new key signs.
	day := int64(24 * 3600)
	r.clock.Store(91 * day)
	r.restart()
	rotated := r.attemptWith(adminUser, testSecret)
	rotated.Body.Close()
	if keyOf(deviceCookieOf(t, rotated)) == keyOf(old) {
		t.Fatal("after ninety-one days the signing key has not rotated")
	}

	lockedOutWith := func(c *http.Cookie) int {
		for i := 0; i < 5; i++ {
			r.attemptWith(adminUser, "a-guess").Body.Close()
		}
		resp := r.attemptWith(adminUser, testSecret, c)
		resp.Body.Close()
		return resp.StatusCode
	}
	// The retired key still verifies: the old cookie opens its own bucket.
	if got := lockedOutWith(old); got != http.StatusOK {
		t.Fatalf("a cookie from the retired key answered %d inside its lifetime, want 200", got)
	}
	// 180 days after the rotation, it no longer does.
	r.clock.Store(91*day + 181*day)
	r.restart()
	if got := lockedOutWith(old); got != http.StatusTooManyRequests {
		t.Fatalf("a cookie from a key retired 181 days ago answered %d, want 429 (untrusted)", got)
	}
}
