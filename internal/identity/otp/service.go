// Package otp is phone sign-in: a six-digit code sent to a Vietnamese mobile
// number and typed back into the portal.
//
// It owns the code and its abuse controls and nothing else. Presence, the grant
// and the NAS authorization are `internal/portal`'s, exactly as they are for a
// voucher — this package answers one question per call: *may a code be sent to
// this number from this browser*, and *is this the code that was sent*.
//
// ── what the field does, and where this diverges ─────────────────────────────
//
// PacketFence's SMS source sends a PIN to the guest's number
// and asks for it back, bounded by an activation timeout and a retry limit;
// UniFi's hotspot and Omada both offer SMS verification through Twilio with a
// configurable code lifetime. None of the three ships a **spend ceiling**, and
// every write-up of SMS pumping since 2022 names the missing ceiling as the
// failure. This package differs from them in three places, each deliberate:
//
//  1. **A daily per-site message ceiling that fails closed** (§9, "a hard global
//     spend cap that fails closed"). Counted in the same transaction that records
//     the code, and refused — not skipped — when it cannot be counted.
//  2. **A destination allowlist of one country's mobile ranges**, not a
//     blocklist of premium prefixes; see NormaliseVN.
//  3. **Presence before the send**, which none of the three does: the portal runs
//     `VerifyPresence` before calling Send, so a device the controller cannot
//     place on the venue's network cannot spend the venue's SMS budget.
//
// Numbers (NIST SP 800-63B §5.1.3 for out-of-band secrets: at least six digits,
// valid for at most ten minutes, attempts limited): six digits, five minutes, five
// wrong attempts per code, three codes an hour and five a day per number, five an
// hour per device, sixty an hour per address. Those are the defaults, and an
// administrator tunes each within bounds in the console. Twilio Verify and Authgear expose the same knobs;
// neither lets one be turned off, and nor does this.
//
// ── the device is the key, the address is a flood bound ───────────────────────
//
// The per-address limit was twenty an hour, and a venue whose portal is reached
// through one NAT presents every guest as one address — so twenty was a venue-wide
// cap on phone sign-ins, refusing the twenty-first guest of a busy hour for
// something no guest did. PacketFence's SMS source has no per-address limit at all;
// its limits are per MAC and per activation. Twilio's own guidance on Verify rate
// limits warns that IP limits misfire behind carrier-grade NAT for the same reason.
//
// So the **device** (the MAC the controller placed on the venue network before any
// send — presence is checked first) is the per-guest key, and the number limits and
// the daily ceiling bound what the venue pays. The address limit stays, because it
// is the only one that sees one host spraying numbers from many spoofed MACs, but it
// is sized as a flood bound rather than a per-guest one: sixty an hour, three
// times what one busy café hour needs, and still an hour of one host's sends that
// the site's daily ceiling bounds from above. A venue behind one NAT that sees more
// raises it in the console (bounds 1–2000); one that sees guests' own addresses may
// lower it.
package otp

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/identity/sms"
	"github.com/kiennt048/foyer/internal/ratelimit"
)

// Limits are the abuse limits. They are the product's defence against toll fraud
// rather than a venue preference, so a venue may tune them only **within bounds**
// (`updatePhoneAbuseControls`, and the CHECKs of migration 00033) and never off.
// A venue that never tuned them gets DefaultLimits; one that did carries its own in
// `Settings.Limits`, read per request, so an edit applies to the next code.
type Limits struct {
	PerNumberHour  int
	PerNumberDay   int
	PerDeviceHour  int
	PerAddressHour int
	MaxAttempts    int
	CodeLifetime   time.Duration
}

// DefaultLimits — see the package comment for where each number comes from.
var DefaultLimits = Limits{
	PerNumberHour:  3,
	PerNumberDay:   5,
	PerDeviceHour:  5,
	PerAddressHour: 60,
	MaxAttempts:    5,
	CodeLifetime:   5 * time.Minute,
}

// Config is what New needs. Store and Keys are required.
type Config struct {
	Store Store
	Keys  Keys
	// Provider builds the adapter for one site's settings. Defaults to sms.New.
	Provider func(sms.Config) (sms.Provider, error)
	Limits   Limits
	Now      func() time.Time
	// Location is the fallback calendar the daily ceiling counts in, for a site
	// whose settings carry none. Defaults to `time.Local`. A site's own zone
	// (`Settings.Location`, from `sites.time_zone`) wins.
	Location *time.Location
	Logger   *slog.Logger
}

// Service is phone sign-in's two operations.
type Service struct {
	store    Store
	keys     Keys
	provider func(sms.Config) (sms.Provider, error)
	limits   Limits
	now      func() time.Time
	loc      *time.Location
	log      *slog.Logger

	// The in-process limits, on `internal/ratelimit`'s fixed window — the same
	// package, the same bound on its map, as the sign-in forms' buckets.
	devices   *ratelimit.Window
	addresses *ratelimit.Window
}

// New builds the service, refusing to without a store or a key.
func New(c Config) (*Service, error) {
	if c.Store == nil || c.Keys == nil {
		return nil, errors.New("otp: a Store and Keys are required")
	}
	s := &Service{
		store: c.Store, keys: c.Keys, provider: c.Provider, limits: c.Limits,
		now: c.Now, loc: c.Location, log: c.Logger,
		devices: ratelimit.NewWindow(), addresses: ratelimit.NewWindow(),
	}
	if s.provider == nil {
		s.provider = sms.New
	}
	if s.limits == (Limits{}) {
		s.limits = DefaultLimits
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s, nil
}

// Limits reports the service's default limits — those in force at a venue that
// has not tuned its own.
func (s *Service) Limits() Limits { return s.limits }

// Offered reports whether the site can send codes, and how long a phone guest is
// granted. A site whose settings cannot be read is not offered.
func (s *Service) Offered(ctx context.Context, siteID string) (minutes int, ok bool) {
	set, err := s.store.PhoneSettings(ctx, siteID)
	if err != nil || set.Provider == "" || set.SessionMinutes < 1 {
		return 0, false
	}
	return set.SessionMinutes, true
}

// Request is one guest asking for a code.
type Request struct {
	SiteID           string
	SessionTokenHash string
	ClientMAC        string
	ClientIP         string
	// Phone is as typed; Send normalises it.
	Phone string
}

// Send issues and sends a code. The caller has already run `VerifyPresence`.
func (s *Service) Send(ctx context.Context, r Request) error {
	set, err := s.store.PhoneSettings(ctx, r.SiteID)
	if err != nil || set.Provider == "" {
		return ErrNotOffered
	}
	phone, err := NormaliseVN(r.Phone)
	if err != nil || !InRanges(phone, set.MobilePrefixes) {
		return ErrPhoneInvalid
	}
	limits := s.limitsOf(set)
	now := s.now()
	// The in-process two first: they cost nothing, and a device hammering the
	// endpoint should be refused before it takes a row lock. The limit is read
	// from this request's settings, so a venue that raised it is not waiting on
	// a restart — the window itself is unchanged by the edit.
	if !s.devices.Allow(DeviceKey(r.ClientMAC, r.ClientIP), limits.PerDeviceHour, time.Hour, now) {
		return ErrLimited
	}
	// Keyed as the sign-in buckets key an address — an IPv6 peer on its /64 —
	// so one subscriber cannot rotate through addresses to reset it.
	if r.ClientIP != "" && !s.addresses.Allow(ratelimit.ClientKey(r.ClientIP), limits.PerAddressHour, time.Hour, now) {
		return ErrLimited
	}

	provider, err := s.providerFor(set)
	if err != nil {
		return ErrNotOffered
	}

	code, err := NewCode()
	if err != nil {
		return err
	}
	id, err := newID()
	if err != nil {
		return err
	}
	keyID, key, err := s.keys.PhoneCodeKey()
	if err != nil {
		return ErrNotOffered
	}
	in := Issue{
		ID: id, SiteID: r.SiteID, SessionTokenHash: r.SessionTokenHash,
		ClientMAC: r.ClientMAC, Phone: phone,
		MAC: MAC(key, id, r.SessionTokenHash, code), KeyID: keyID,
		At: now, ExpiresAt: now.Add(limits.CodeLifetime), MaxAttempts: limits.MaxAttempts,
		Day: s.DayOf(set.Location, now), DailyLimit: set.DailyLimit, GlobalLimit: set.GlobalDailyLimit,
		PerNumberHour: limits.PerNumberHour, PerNumberDay: limits.PerNumberDay,
	}
	if err := s.store.Issue(ctx, in); err != nil {
		switch {
		case errors.Is(err, ErrLimited):
			return ErrLimited
		case errors.Is(err, ErrCeiling):
			s.log.Warn("otp: the daily message ceiling refused a send", "site", r.SiteID)
			return ErrCeiling
		default:
			// A ceiling that could not be counted is a ceiling that is reached.
			s.log.Error("otp: the send could not be recorded, so it was not made", "site", r.SiteID, "err", err)
			return ErrCeiling
		}
	}

	msg := sms.Message{To: phone, Code: code, Text: Render(set.MessageTemplate, code), RequestID: id}
	if err := provider.Send(ctx, msg); err != nil {
		// Retired so that a code which may never have arrived cannot be guessed
		// at either. The ceiling keeps the count: whether the provider charged for
		// a refused message is the provider's business, and over-counting is the
		// safe direction for a cost ceiling.
		_ = s.store.Retire(ctx, id, now)
		s.log.Warn("otp: the provider did not accept a code", "site", r.SiteID, "code_id", id, "err", err)
		return ErrSendFailed
	}
	// The id and the site only. Not the number and never the code.
	s.log.Info("otp: code sent", "site", r.SiteID, "code_id", id)
	return nil
}

// Check spends one attempt on this browser's live code and answers with the
// number it was sent to.
func (s *Service) Check(ctx context.Context, sessionTokenHash, submitted string) (phone string, err error) {
	code := NormaliseCode(submitted)
	if code == "" {
		// Not six digits: nothing to compare, so no attempt is spent on it. A
		// guest who typed a letter has not guessed.
		return "", ErrCodeWrong
	}
	now := s.now()
	a, err := s.store.Attempt(ctx, sessionTokenHash, now)
	if err != nil {
		return "", ErrCodeExpired
	}
	key, err := s.keys.PhoneCodeKeyByID(a.KeyID)
	if err != nil {
		return "", ErrCodeExpired
	}
	if !SameMAC(MAC(key, a.ID, sessionTokenHash, code), a.MAC) {
		if a.Attempts >= a.MaxAttempts {
			// The last attempt was spent. The code is dead, and the guest's
			// next step is the same as for an expired one.
			return "", ErrCodeExpired
		}
		return "", ErrCodeWrong
	}
	if err := s.store.Consume(ctx, a.ID, now); err != nil {
		// Somebody else with the same code won. Single use means one of them.
		return "", ErrCodeExpired
	}
	return a.Phone, nil
}

// DeviceKey is the per-device limit's key: the MAC the controller placed on the
// network **together with the connection source it was claimed from**.
//
// The MAC alone is not enough, and presence does not make it safe: `VerifyPresence` proves a MAC is on the network, not
// that the caller *is* that device. MACs are visible over the air, so anybody in
// radio range could claim a present guest's MAC and spend that guest's five codes
// an hour — and the guest, asking for their own code, would be told they had
// asked too often.
//
// The source is the one part of the request the caller cannot type: the TCP peer,
// an IPv6 peer on its /64 (`ratelimit.ClientKey`, the key the per-address limit
// already uses). A stranger claiming a guest's MAC from their own laptop now spends
// a bucket of their own, and that laptop's claims are bounded, all together, by the
// per-address limit — the budget a one-host spray had before this too.
//
// **Not the portal cookie, deliberately.** A cookie is free to mint: deleting it
// and arriving again is a fresh session and would be a fresh bucket, so a device
// keyed on its session is a device with no limit. Twilio Verify's rate-limit
// guidance and PacketFence's SMS source land in the same place — throttle on the
// identifier the service observed, never on one the client presents.
//
// The residue, stated: guests behind one NAT share a source, so a stranger behind
// the same NAT can still spend a guest's bucket. That is the NAT case
// `internal/portal/address.go` already treats as one ambiguous address.
func DeviceKey(mac, clientIP string) string {
	if clientIP == "" {
		return "mac:" + mac
	}
	return "mac:" + mac + "|" + ratelimit.ClientKey(clientIP)
}

func (s *Service) providerFor(set Settings) (sms.Provider, error) {
	return s.provider(sms.Config{
		Kind: set.Provider, APIKey: set.APIKey, SecretKey: set.SecretKey,
		Brandname: set.Brandname, ZaloOAID: set.ZaloOAID, ZaloTemplateID: set.ZaloTemplateID,
		Sandbox: set.Sandbox,
	})
}

func (s *Service) limitsOf(set Settings) Limits {
	if set.Limits != nil {
		return *set.Limits
	}
	return s.limits
}

// DayOf is the calendar day t falls on at a site whose zone is loc — the day the
// ceilings count against. A nil loc is the service's own Location, the fallback
// for a store that cannot name the site's zone.
func (s *Service) DayOf(loc *time.Location, t time.Time) string {
	if loc == nil {
		loc = s.loc
	}
	return t.In(loc).Format("2006-01-02")
}

// ── the console's test message ───────────────────────────────────────────────

// The outcomes of a test message, matching the contract's `PhoneTestMessageOutcome`.
const (
	TestSent                = "sent"
	TestPhoneInvalid        = "phone-invalid"
	TestNotConfigured       = "not-configured"
	TestCeilingReached      = "ceiling-reached"
	TestProviderRefused     = "provider-refused"
	TestProviderUnreachable = "provider-unreachable"
)

// TestResult is what a test message came to.
type TestResult struct {
	Outcome string
	// ProviderCode is the provider's result code on TestProviderRefused.
	ProviderCode string
}

// SendTest sends one message through the site's saved provider to the operator's
// own number: the template with a fresh random code that is stored nowhere and
// signs nobody in.
//
// **It is charged like a code.** The same allowlist, and the same two ceilings
// counted before the send and failing closed — a test message is a real message
// the venue pays for, and a console button that bypassed the spend cap would be
// the cheapest way round it. The per-number, per-device and per-address limits are
// a guest's and do not apply: the caller is an administrator, gated and audited.
func (s *Service) SendTest(ctx context.Context, siteID, rawPhone string) TestResult {
	set, err := s.store.PhoneSettings(ctx, siteID)
	if err != nil || set.Provider == "" {
		return TestResult{Outcome: TestNotConfigured}
	}
	phone, err := NormaliseVN(rawPhone)
	if err != nil || !InRanges(phone, set.MobilePrefixes) {
		return TestResult{Outcome: TestPhoneInvalid}
	}
	provider, err := s.providerFor(set)
	if err != nil {
		return TestResult{Outcome: TestNotConfigured}
	}
	code, err := NewCode()
	if err != nil {
		return TestResult{Outcome: TestNotConfigured}
	}
	id, err := newID()
	if err != nil {
		return TestResult{Outcome: TestNotConfigured}
	}
	if err := s.store.Count(ctx, CountedSend{
		SiteID: siteID, Day: s.DayOf(set.Location, s.now()),
		DailyLimit: set.DailyLimit, GlobalLimit: set.GlobalDailyLimit,
	}); err != nil {
		return TestResult{Outcome: TestCeilingReached}
	}
	err = provider.Send(ctx, sms.Message{To: phone, Code: code, Text: Render(set.MessageTemplate, code), RequestID: id})
	switch {
	case err == nil:
		s.log.Info("otp: test message sent", "site", siteID, "request_id", id)
		return TestResult{Outcome: TestSent}
	case errors.Is(err, sms.ErrUnreachable):
		return TestResult{Outcome: TestProviderUnreachable}
	default:
		return TestResult{Outcome: TestProviderRefused, ProviderCode: sms.RefusalCode(err)}
	}
}

// Render puts the code into the template. The first `{code}` only; the API
// refuses a template without exactly one.
func Render(template, code string) string {
	return strings.Replace(template, "{code}", code, 1)
}

// ValidTemplate reports whether a template carries exactly one `{code}`.
func ValidTemplate(t string) bool { return strings.Count(t, "{code}") == 1 }
