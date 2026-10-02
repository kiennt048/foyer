package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The console's trusted-device cookie.
//
// ── the gap it closes ─────────────────────────────────────────────────────────
//
// The per-account sign-in bucket (`hardening.go`) is five attempts and then one
// every thirty seconds **against a username, from anywhere**. The console is
// published on every interface, so one device on the venue LAN spending two
// requests a minute keeps `admin` answering 429 indefinitely — and reset-on-success
// cannot help, because the owner never gets a success. Keycloak's per-user lockout
// has exactly this gap and documents it.
//
// ── what the field does ───────────────────────────────────────────────────────
//
// OWASP's *Slow Down Online Guessing Attacks with Device Cookies* is the remedy the
// field converged on, and it is the shape here: a browser that has signed in to an
// account before carries a signed cookie naming that account; an attempt for that
// account carrying a valid one spends from **that device's own bucket**, and every
// other attempt spends from the account's shared "untrusted" bucket. The attacker
// can still empty the shared bucket — which now refuses only browsers that never
// signed in as that account — and cannot touch the owner's. authentik reaches a
// similar place differently (its reputation score is per IP and per username, so a
// known-good address keeps working); Keycloak has no equivalent. A per-account+IP
// bucket was the other candidate and was not taken: the owner's own address is
// not stable (DHCP, a phone on 4G), and an attacker on the same NAT as the owner
// shares it.
//
// ── what it is ────────────────────────────────────────────────────────────────
//
// `v2.<key id>.<account digest>.<nonce>.<HMAC-SHA256>`. Not a session and grants
// nothing: it only chooses which bucket an attempt spends from, and the password is
// still verified. `__Host-` prefixed, `Secure`, `HttpOnly`, `SameSite=Strict`, 180
// days. The nonce makes each device's bucket its own and bounds the key's size, and
// a cookie that does not verify is ignored — so nobody can mint bucket keys to
// flood the limiter.
//
// ── the key, and why it is persisted ─────────────────────────────────────────
//
// A key drawn from `crypto/rand` when the process starts rests on the theory that
// a restart costs the owner one wait at most. That theory misses something: **a
// restart or an upgrade is exactly when a stranger already emptying the shared
// bucket locks the owner out**, because every trusted device becomes untrusted at
// once and the only bucket left is the one the stranger holds. The defence would
// fail at the moment it was needed.
//
// So the key ring lives in the database (`device_cookie_keys`, migration
// 00053), each key sealed under FOYER_KEK like every other secret there, through
// `Ports.DeviceKeys`. The shape is the one key-ring systems in the field share —
// ASP.NET Core Data Protection (a 90-day key lifetime, retired keys kept to
// *verify*), Django's `SECRET_KEY_FALLBACKS`, Keycloak's realm keys with an active
// and a passive set:
//
//   - **one active key signs**, and it is replaced when it is `deviceKeyRotateAfter`
//     old — by a deliberate policy, never by a restart;
//   - **a retired key still verifies** for `deviceCookieLifetime` after its
//     successor arrived, so a cookie issued the minute before a rotation is honoured
//     for its whole life;
//   - the cookie names the key it was signed with, so verification needs no guess.
//
// The ring is read through the port and cached for `deviceKeyRefresh`, so a running
// process picks up a rotation (its own or a second process's) without a restart.
// A process with no port — a test server, or an API with no database — keeps the
// old per-process key, which is the most it could do.

const (
	deviceCookieName     = "__Host-foyer_device"
	deviceCookieLifetime = 180 * 24 * time.Hour
	deviceCookieVersion  = "v2"

	// deviceKeyRotateAfter is the active key's lifetime: ASP.NET Core Data
	// Protection's default, and well inside the cookie's own 180 days.
	deviceKeyRotateAfter = 90 * 24 * time.Hour
	// deviceKeyRefresh is how long a process trusts the ring it last read.
	deviceKeyRefresh = time.Hour
	// deviceKeyRetry bounds how often a failed read is retried, so a database
	// outage is not one query per sign-in attempt.
	deviceKeyRetry = 10 * time.Second
)

// DeviceCookieKey is one key of the trusted-device ring.
type DeviceCookieKey struct {
	ID  string
	Key []byte
}

// DeviceCookieKeyStore persists the trusted-device key ring.
//
// `DeviceCookieKeys` answers the usable keys, **newest (the signing key) first**,
// having first — in one transaction, so two processes agree — minted a new key when
// there is none or the newest is older than `rotateAfter`, and dropped every key
// whose successor has been active longer than `keepFor`. A key that cannot be
// unsealed (its KEK left the keyring) is not answered, and a new one is minted if
// that leaves none: a device that was trusted becomes untrusted once, which is the
// fail-safe direction.
type DeviceCookieKeyStore interface {
	DeviceCookieKeys(ctx context.Context, now time.Time, rotateAfter, keepFor time.Duration) ([]DeviceCookieKey, error)
}

type deviceCookies struct {
	store DeviceCookieKeyStore
	now   func() time.Time

	mu      sync.Mutex
	ring    []DeviceCookieKey
	fresh   time.Time // the ring is good until then
	retryAt time.Time // after a failed read, not before then
}

func newDeviceCookies(store DeviceCookieKeyStore, now func() time.Time) *deviceCookies {
	d := &deviceCookies{store: store, now: now}
	if store == nil {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err == nil {
			// crypto/rand does not fail on a supported platform; a zero key would
			// be a forgeable cookie, so on failure there is no key instead.
			d.ring = []DeviceCookieKey{{ID: "process", Key: key}}
		}
	}
	return d
}

// keys answers the ring in force, reading it from the store when the cached one
// is stale. On a failed read the last ring is kept; with none, there is no key —
// no cookie is issued and none is trusted, so every attempt spends from the
// shared bucket, which is what this process did before it had a cookie at all.
func (d *deviceCookies) keys(ctx context.Context) []DeviceCookieKey {
	if d.store == nil {
		return d.ring
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if now.Before(d.fresh) || now.Before(d.retryAt) {
		return d.ring
	}
	ring, err := d.store.DeviceCookieKeys(ctx, now, deviceKeyRotateAfter, deviceCookieLifetime)
	if err != nil || len(ring) == 0 {
		d.retryAt = now.Add(deviceKeyRetry)
		return d.ring
	}
	d.ring, d.fresh = ring, now.Add(deviceKeyRefresh)
	return d.ring
}

func deviceMAC(key []byte, keyID, account, nonce string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(deviceCookieVersion + "|" + keyID + "|" + account + "|" + nonce))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// issue mints a fresh cookie value for the account key under the active key, or
// "" when there is none.
func (d *deviceCookies) issue(ctx context.Context, account string) string {
	ring := d.keys(ctx)
	if len(ring) == 0 || len(ring[0].Key) == 0 {
		return ""
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	k := ring[0]
	return strings.Join([]string{deviceCookieVersion, k.ID, account, nonce, deviceMAC(k.Key, k.ID, account, nonce)}, ".")
}

// deviceFor answers the device's nonce when the request carries a cookie signed by
// a key in the ring **for this account**, and "" otherwise.
func (d *deviceCookies) deviceFor(r *http.Request, account string) string {
	c, err := r.Cookie(deviceCookieName)
	if err != nil || len(c.Value) > 256 {
		return ""
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 5 || parts[0] != deviceCookieVersion || parts[2] != account {
		return ""
	}
	for _, k := range d.keys(r.Context()) {
		if k.ID != parts[1] || len(k.Key) == 0 {
			continue
		}
		if hmac.Equal([]byte(parts[4]), []byte(deviceMAC(k.Key, k.ID, parts[2], parts[3]))) {
			return parts[3]
		}
		return ""
	}
	return ""
}

func setDeviceCookie(w http.ResponseWriter, value string, now time.Time) {
	if value == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     deviceCookieName,
		Value:    value,
		Path:     "/",
		Expires:  now.Add(deviceCookieLifetime),
		MaxAge:   int(deviceCookieLifetime.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}
