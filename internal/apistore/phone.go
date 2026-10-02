package apistore

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity/otp"
	"github.com/kiennt048/foyer/internal/store"
)

// Phone sign-in over PostgreSQL: the operator's configuration (`api.PhoneSignInStore`),
// the code store (`otp.Store`), and the guest seam (`portal.PhoneCodes`).
//
// **One `otp.Service` per Adapter**, built in `New`, for the reason `PortalLimiter`
// is on the adapter: `cmd/foyer` builds two `portal.Service` values over one seam,
// and the per-device and per-address windows live in the service — two services
// would be two budgets for one guest.

var (
	_ api.PhoneSignInStore = (*Adapter)(nil)
	_ otp.Store            = phoneCodeStore{}
)

func newPhoneService(a *Adapter) *otp.Service {
	svc, err := otp.New(otp.Config{
		Store: phoneCodeStore{a: a}, Keys: phoneKeys{st: a.st}, Now: a.now, Provider: a.smsProvider,
	})
	if err != nil {
		// Store and Keys are always supplied here; New refuses only a nil one.
		panic("apistore: " + err.Error())
	}
	return svc
}

// phoneKeys reaches the keyring through the store on every call, so a store opened
// with no FOYER_KEK answers ErrNoKeyring — which otp turns into *not offered* —
// rather than a nil pointer somewhere nobody is looking.
type phoneKeys struct{ st *store.Store }

func (k phoneKeys) PhoneCodeKey() (string, []byte, error) { return k.st.Keys().PhoneCodeKey() }
func (k phoneKeys) PhoneCodeKeyByID(id string) ([]byte, error) {
	return k.st.Keys().PhoneCodeKeyByID(id)
}

// ── the operator's view ──────────────────────────────────────────────────────

// PhoneSignIn reads the configuration and today's count. An unconfigured site is
// `provider: null`; an unknown one is ErrNotFound.
func (a *Adapter) PhoneSignIn(ctx context.Context, siteID string) (gen.PhoneSignIn, error) {
	if _, err := a.st.Sites().Get(ctx, siteID); err != nil {
		return gen.PhoneSignIn{}, out(err)
	}
	p, err := a.st.Phone().Get(ctx, siteID)
	if errors.Is(err, store.ErrNotFound) {
		return gen.PhoneSignIn{SiteId: siteID}, nil
	}
	if err != nil {
		return gen.PhoneSignIn{}, out(err)
	}
	sent, err := a.st.Phone().SentOn(ctx, siteID, a.phone.DayOf(a.location(ctx, siteID), a.now()))
	if err != nil {
		return gen.PhoneSignIn{}, out(err)
	}
	return phoneOut(p, sent), nil
}

// SetPhoneSignIn replaces the configuration, sealing the keys it was given.
func (a *Adapter) SetPhoneSignIn(ctx context.Context, siteID string, in gen.PhoneSignInUpdate) (gen.PhoneSignIn, error) {
	if _, err := a.st.Sites().Get(ctx, siteID); err != nil {
		return gen.PhoneSignIn{}, out(err)
	}
	p := &store.PhoneSignIn{
		SiteID: siteID, Provider: string(in.Provider),
		DailyLimit: in.DailyLimit, SessionMinutes: in.SessionMinutes, MessageTemplate: in.MessageTemplate,
	}
	// Only the fields the provider reads, so a site switched from SMS to Zalo
	// does not keep a brandname nothing will ever send from.
	switch in.Provider {
	case gen.EsmsSms:
		p.Brandname = in.Brandname
	case gen.EsmsZalo:
		p.ZaloOAID, p.ZaloTemplateID = in.ZaloOaId, in.ZaloTemplateId
	}
	if in.Sandbox != nil {
		p.Sandbox = *in.Sandbox
	}
	var apiKey, secretKey []byte
	if in.ApiKey != nil {
		apiKey = []byte(*in.ApiKey)
	}
	if in.SecretKey != nil {
		secretKey = []byte(*in.SecretKey)
	}
	if err := a.st.Phone().Upsert(ctx, p, apiKey, secretKey); err != nil {
		if errors.Is(err, store.ErrSecretUnset) {
			return gen.PhoneSignIn{}, api.ErrKeysRequired
		}
		return gen.PhoneSignIn{}, out(err)
	}
	return a.PhoneSignIn(ctx, siteID)
}

func phoneOut(p *store.PhoneSignIn, sent int) gen.PhoneSignIn {
	provider := gen.PhoneSignInProvider(p.Provider)
	return gen.PhoneSignIn{
		SiteId:          p.SiteID,
		Provider:        &provider,
		Brandname:       p.Brandname,
		ZaloOaId:        p.ZaloOAID,
		ZaloTemplateId:  p.ZaloTemplateID,
		CredentialsSet:  true, // the row cannot exist without both: see PhoneRepo.Upsert
		Sandbox:         p.Sandbox,
		DailyLimit:      &p.DailyLimit,
		SentToday:       sent,
		SessionMinutes:  &p.SessionMinutes,
		MessageTemplate: &p.MessageTemplate,
	}
}

// ── abuse controls, the spend cap, the test message ──────────────────────────

func (a *Adapter) PhoneAbuseControls(ctx context.Context, siteID string) (gen.PhoneAbuseControls, error) {
	if _, err := a.st.Sites().Get(ctx, siteID); err != nil {
		return gen.PhoneAbuseControls{}, out(err)
	}
	c, err := a.st.Phone().AbuseControls(ctx, siteID)
	if errors.Is(err, store.ErrNotFound) {
		return abuseDefaults(siteID), nil
	}
	if err != nil {
		return gen.PhoneAbuseControls{}, out(err)
	}
	return abuseOut(c), nil
}

func (a *Adapter) SetPhoneAbuseControls(ctx context.Context, siteID string, in gen.PhoneAbuseControlsUpdate) (gen.PhoneAbuseControls, error) {
	if _, err := a.st.Sites().Get(ctx, siteID); err != nil {
		return gen.PhoneAbuseControls{}, out(err)
	}
	l := in.Limits
	c := &store.PhoneAbuseControls{
		SiteID: siteID, PerNumberHour: l.PerNumberPerHour, PerNumberDay: l.PerNumberPerDay,
		PerDeviceHour: l.PerDevicePerHour, PerAddressHour: l.PerAddressPerHour,
		CodeMinutes: l.CodeMinutes, MaxAttempts: l.MaxAttempts,
		MobilePrefixes: in.MobilePrefixes,
	}
	if err := a.st.Phone().SetAbuseControls(ctx, c); err != nil {
		return gen.PhoneAbuseControls{}, out(err)
	}
	return abuseOut(c), nil
}

func (a *Adapter) PhoneSpendCap(ctx context.Context) (gen.PhoneSpendCap, error) {
	limit, at, err := a.st.Phone().SpendCap(ctx)
	if err != nil {
		return gen.PhoneSpendCap{}, out(err)
	}
	c := gen.PhoneSpendCap{DailyLimit: limit}
	if !at.IsZero() {
		c.UpdatedAt = &at
	}
	return c, nil
}

func (a *Adapter) SetPhoneSpendCap(ctx context.Context, dailyLimit *int) (gen.PhoneSpendCap, error) {
	at, err := a.st.Phone().SetSpendCap(ctx, dailyLimit)
	if err != nil {
		return gen.PhoneSpendCap{}, out(err)
	}
	return gen.PhoneSpendCap{DailyLimit: dailyLimit, UpdatedAt: &at}, nil
}

func (a *Adapter) SendPhoneTestMessage(ctx context.Context, siteID, phone string) (gen.PhoneTestMessageResult, error) {
	if _, err := a.st.Sites().Get(ctx, siteID); err != nil {
		return gen.PhoneTestMessageResult{}, out(err)
	}
	res := a.phone.SendTest(ctx, siteID, phone)
	sent, err := a.st.Phone().SentOn(ctx, siteID, a.phone.DayOf(a.location(ctx, siteID), a.now()))
	if err != nil {
		return gen.PhoneTestMessageResult{}, out(err)
	}
	return testResultOut(res, sent), nil
}

// testResultOut is otp's result on the wire. Shared with `internal/api/memory` in
// spirit only: that package restates it, because it cannot import this one.
func testResultOut(res otp.TestResult, sent int) gen.PhoneTestMessageResult {
	r := gen.PhoneTestMessageResult{Outcome: gen.PhoneTestMessageOutcome(res.Outcome), SentToday: sent}
	if res.ProviderCode != "" {
		code := res.ProviderCode
		r.ProviderCode = &code
	}
	return r
}

func abuseDefaults(siteID string) gen.PhoneAbuseControls {
	return gen.PhoneAbuseControls{
		SiteId: siteID, Limits: api.PhoneLimits(otp.DefaultLimits),
		MobilePrefixes: slices.Clone(otp.MobilePrefixes),
		Country:        gen.PhoneAbuseControlsCountryVN,
	}
}

func abuseOut(c *store.PhoneAbuseControls) gen.PhoneAbuseControls {
	at := c.UpdatedAt
	return gen.PhoneAbuseControls{
		SiteId:         c.SiteID,
		Limits:         api.PhoneLimits(abuseLimits(c)),
		MobilePrefixes: slices.Clone(c.MobilePrefixes),
		Country:        gen.PhoneAbuseControlsCountryVN,
		Customised:     true,
		UpdatedAt:      &at,
	}
}

func abuseLimits(c *store.PhoneAbuseControls) otp.Limits {
	return otp.Limits{
		PerNumberHour: c.PerNumberHour, PerNumberDay: c.PerNumberDay,
		PerDeviceHour: c.PerDeviceHour, PerAddressHour: c.PerAddressHour,
		MaxAttempts: c.MaxAttempts, CodeLifetime: time.Duration(c.CodeMinutes) * time.Minute,
	}
}

// ── otp.Store ────────────────────────────────────────────────────────────────

type phoneCodeStore struct{ a *Adapter }

func (s phoneCodeStore) PhoneSettings(ctx context.Context, siteID string) (otp.Settings, error) {
	p, err := s.a.st.Phone().Get(ctx, siteID)
	if err != nil {
		return otp.Settings{}, otp.ErrNotOffered
	}
	apiKey, secretKey, err := s.a.st.Phone().RevealKeys(ctx, siteID)
	if err != nil {
		return otp.Settings{}, otp.ErrNotOffered
	}
	set := otp.Settings{
		Provider: p.Provider, Brandname: deref(p.Brandname),
		ZaloOAID: deref(p.ZaloOAID), ZaloTemplateID: deref(p.ZaloTemplateID),
		APIKey: string(apiKey), SecretKey: string(secretKey), Sandbox: p.Sandbox,
		DailyLimit: p.DailyLimit, SessionMinutes: p.SessionMinutes, MessageTemplate: p.MessageTemplate,
		Location: s.a.location(ctx, siteID),
	}
	// The venue's tuned limits and ranges, read with every send so an edit in the
	// console applies to the next code. A read that fails is not the defaults:
	// it is a send that is not made, for the reason the ceiling fails closed.
	switch c, err := s.a.st.Phone().AbuseControls(ctx, siteID); {
	case err == nil:
		l := abuseLimits(c)
		set.Limits, set.MobilePrefixes = &l, c.MobilePrefixes
	case !errors.Is(err, store.ErrNotFound):
		return otp.Settings{}, otp.ErrNotOffered
	}
	global, _, err := s.a.st.Phone().SpendCap(ctx)
	if err != nil {
		return otp.Settings{}, otp.ErrNotOffered
	}
	set.GlobalDailyLimit = global
	return set, nil
}

func (s phoneCodeStore) Count(ctx context.Context, c otp.CountedSend) error {
	err := s.a.st.Phone().CountSend(ctx, c.SiteID, c.Day, c.DailyLimit, c.GlobalLimit)
	if errors.Is(err, store.ErrPhoneCeiling) {
		return otp.ErrCeiling
	}
	return err
}

func (s phoneCodeStore) Issue(ctx context.Context, in otp.Issue) error {
	err := s.a.st.Phone().Issue(ctx, store.PhoneCodeIssue{
		ID: in.ID, SiteID: in.SiteID, SessionTokenHash: in.SessionTokenHash, ClientMAC: in.ClientMAC,
		Phone: in.Phone, CodeMAC: in.MAC, KeyID: in.KeyID, At: in.At, ExpiresAt: in.ExpiresAt,
		MaxAttempts: in.MaxAttempts, Day: in.Day, DailyLimit: in.DailyLimit, GlobalLimit: in.GlobalLimit,
		PerNumberHour: in.PerNumberHour, PerNumberDay: in.PerNumberDay,
	})
	switch {
	case errors.Is(err, store.ErrPhoneLimited):
		return otp.ErrLimited
	case errors.Is(err, store.ErrPhoneCeiling):
		return otp.ErrCeiling
	default:
		return err
	}
}

func (s phoneCodeStore) Attempt(ctx context.Context, sessionTokenHash string, at time.Time) (otp.Attempt, error) {
	a, err := s.a.st.Phone().Attempt(ctx, sessionTokenHash, at)
	if err != nil {
		return otp.Attempt{}, otp.ErrCodeExpired
	}
	return otp.Attempt{ID: a.ID, Phone: a.Phone, MAC: a.CodeMAC, KeyID: a.KeyID,
		Attempts: a.Attempts, MaxAttempts: a.MaxAttempts}, nil
}

func (s phoneCodeStore) Consume(ctx context.Context, id string, at time.Time) error {
	if err := s.a.st.Phone().Consume(ctx, id, at); err != nil {
		return otp.ErrCodeExpired
	}
	return nil
}

func (s phoneCodeStore) Retire(ctx context.Context, id string, at time.Time) error {
	if err := s.a.st.Phone().Consume(ctx, id, at); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}
