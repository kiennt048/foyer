package api

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// DefaultPageLimit and MaxPageLimit are the contract's own numbers:
// `components.parameters.Limit` declares `minimum: 1, maximum: 200, default: 50`.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// windowFrom clamps what a client asked for into the contract's range.
//
// It clamps rather than refuses, which is what `web/mock` does and what the
// contract implies by declaring bounds on a parameter and no `4xx` for
// exceeding them. A client that asks for 10 000 rows gets 200 and a
// `nextCursor`, which is a working answer; a refusal would be a screen that
// renders nothing because somebody typed a number.
func windowFrom(cursor *gen.Cursor, limit *gen.Limit) Window {
	w := Window{Limit: DefaultPageLimit}
	if cursor != nil {
		w.Cursor = string(*cursor)
	}
	if limit != nil {
		w.Limit = int(*limit)
		if w.Limit < 1 {
			w.Limit = 1
		}
		if w.Limit > MaxPageLimit {
			w.Limit = MaxPageLimit
		}
	}
	return w
}

// text dereferences an optional string query parameter.
func text(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// csv splits a `style: form, explode: false` array parameter — `state=a,b,c`.
// The contract uses that style on `listSessions.state` and `listVouchers.state`.
func csv[T ~string](raw *string) []T {
	if raw == nil || *raw == "" {
		return nil
	}
	parts := strings.Split(*raw, ",")
	out := make([]T, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, T(p))
		}
	}
	return out
}

// ── identifiers and secrets ──────────────────────────────────────────────────

// defaultID mints an identifier.
//
// crypto/rand, and base32 without padding so an id is safe in a path segment and
// in a `clients.conf` name. A sequential id would leak how many of a thing a
// deployment has, which is a small thing, and would collide across two processes
// writing to one database, which is not.
func defaultID(prefix string) string {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand does not fail on any platform this runs on; if it ever
		// does, an id derived from the clock would be worse than stopping.
		panic(fmt.Sprintf("api: crypto/rand: %v", err))
	}
	return prefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
}

// defaultSecret mints a RADIUS shared secret.
//
// There is no default secret and never a fallback: FreeRADIUS ships an example
// secret in its sample configuration and shipped example credentials are how
// appliance software gets mass-owned (`docs/rules/security.md` §7). This
// returns an error rather than a placeholder when randomness is unavailable.
func defaultSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

func ptr[T any](v T) *T { return &v }

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
