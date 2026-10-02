package otp

import (
	"context"
	"errors"
	"time"
)

// The errors a guest's request can end in. `internal/portal` maps each onto a
// sentence; nothing here is ever shown to anybody as text.
var (
	// ErrNotOffered — the site has no provider configured, or no keyring to
	// unseal its keys with.
	ErrNotOffered = errors.New("otp: phone sign-in is not configured here")
	// ErrPhoneInvalid — not a Vietnamese mobile number. See NormaliseVN.
	ErrPhoneInvalid = errors.New("otp: not a number codes can be sent to")
	// ErrLimited — a per-number, per-device or per-address limit was reached.
	ErrLimited = errors.New("otp: too many codes requested")
	// ErrCeiling — the site's daily message ceiling was reached, **or could not
	// be read**. The ceiling fails closed: a count nobody can confirm is a count
	// that is spent.
	ErrCeiling = errors.New("otp: the daily message ceiling is reached")
	// ErrSendFailed — the provider refused or could not be reached. The code that
	// was issued is retired, so it cannot be guessed at.
	ErrSendFailed = errors.New("otp: the code could not be sent")

	// ErrCodeWrong — a code that does not match, or is not six digits.
	ErrCodeWrong = errors.New("otp: wrong code")
	// ErrCodeExpired — no live code for this browser: never sent, past its
	// lifetime, already used, retired by a newer one, or out of attempts. One
	// answer for all five, because every one of them ends the same way — ask for
	// a new code — and telling them apart would say which codes exist.
	ErrCodeExpired = errors.New("otp: no live code")
)

// Settings is one site's configuration, with the provider's keys unsealed. It is
// read per request and lives no longer than one; it is never logged.
type Settings struct {
	Provider       string
	Brandname      string
	ZaloOAID       string
	ZaloTemplateID string
	APIKey         string
	SecretKey      string
	Sandbox        bool

	DailyLimit      int
	SessionMinutes  int
	MessageTemplate string

	// GlobalDailyLimit is the deployment-wide cap across every site; nil is none.
	GlobalDailyLimit *int
	// Location is the site's calendar (`sites.time_zone`): where the daily
	// ceilings' day starts. Nil falls back to the service's Location.
	Location *time.Location
	// Limits are the venue's tuned abuse limits; nil is the service's.
	Limits *Limits
	// MobilePrefixes narrows the destination allowlist to these ranges of the
	// Vietnamese mobile plan (`03`, `05`, `07`, `08`, `09`); empty is all of them.
	MobilePrefixes []string
}

// String redacts, so a `%v` of the settings cannot print the keys.
func (s Settings) String() string { return "otp.Settings{Provider:" + s.Provider + " keys:[redacted]}" }

// Issue is one code about to be sent, and the limits it must fit inside.
type Issue struct {
	ID               string
	SiteID           string
	SessionTokenHash string
	ClientMAC        string
	Phone            string
	MAC              []byte
	KeyID            string
	At               time.Time
	ExpiresAt        time.Time
	MaxAttempts      int

	// Day is the calendar date the ceiling is counted against, `YYYY-MM-DD` in
	// the deployment's time zone.
	Day           string
	DailyLimit    int
	GlobalLimit   *int
	PerNumberHour int
	PerNumberDay  int
}

// CountedSend is one message that is not a code — the console's test message —
// charged against the same two ceilings a code is.
type CountedSend struct {
	SiteID      string
	Day         string
	DailyLimit  int
	GlobalLimit *int
}

// Attempt is what spending one attempt on a live code returns: enough to check it,
// and the number it was sent to.
type Attempt struct {
	ID          string
	Phone       string
	MAC         []byte
	KeyID       string
	Attempts    int
	MaxAttempts int
}

// Store is where codes and the ceiling live. The SQL implementation is
// `internal/store`'s `PhoneCodeRepo`; `Memory` is the in-process one.
//
// ── three methods, each one statement's worth of guarantee ───────────────────
//
//   - **Issue is the whole send decision, atomically**: the per-number limits, the
//     daily ceiling and the new row, under one lock per number, so two requests in
//     the same millisecond cannot both fit under a limit of one. It also retires
//     every earlier live code for the same browser — asking again must not
//     multiply the attempts available.
//   - **Attempt spends before it reveals.** It increments the attempt count of
//     the newest live code and returns its MAC in the same statement, refusing
//     once the count is spent, so a hundred parallel guesses get `MaxAttempts`
//     comparisons and not a hundred.
//   - **Consume is single use**, a conditional UPDATE: of two requests carrying
//     the right code, one wins (§9, "single-use enforced transactionally, not with
//     a read-then-write").
type Store interface {
	PhoneSettings(ctx context.Context, siteID string) (Settings, error)
	Issue(ctx context.Context, in Issue) error
	Attempt(ctx context.Context, sessionTokenHash string, at time.Time) (Attempt, error)
	Consume(ctx context.Context, id string, at time.Time) error
	Retire(ctx context.Context, id string, at time.Time) error
	// Count charges a CountedSend against both ceilings, atomically, answering
	// ErrCeiling at either — and an error when it cannot count, which the
	// service treats as the ceiling.
	Count(ctx context.Context, c CountedSend) error
}

// Keys derives the MAC key. `internal/store.Keyring` implements it, from
// `FOYER_KEK`; the id names which KEK, so a code issued before a rotation still
// verifies against the key it was made with.
type Keys interface {
	PhoneCodeKey() (id string, key []byte, err error)
	PhoneCodeKeyByID(id string) ([]byte, error)
}
