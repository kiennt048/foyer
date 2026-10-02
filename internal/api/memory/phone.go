package memory

import (
	"context"
	"sync"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity/otp"
	"github.com/kiennt048/foyer/internal/identity/sms"
	"github.com/kiennt048/foyer/internal/portal"
)

// Phone sign-in over the world: the real `otp.Service` over its in-memory store,
// with `sms.Fake` as the provider. **The fake cannot be selected by any saved
// configuration** — the provider is fixed here, in the test world, and nowhere
// else — so this is the one place a code is readable, and it is readable only to a
// test holding this value.

var _ api.PhoneSignInStore = (*Store)(nil)

// PhoneWorld is phone sign-in's state. Exposed so a test can read what was sent.
type PhoneWorld struct {
	mu      sync.Mutex
	Codes   *otp.Memory
	SMS     *sms.Fake
	Service *otp.Service
	config  map[string]gen.PhoneSignIn
	abuse   map[string]gen.PhoneAbuseControls
	cap     gen.PhoneSpendCap
	zones   map[string]string
	// zoneAt is when each site's zone last changed; absent for one nobody set.
	zoneAt map[string]time.Time
}

// Phone returns this store's phone sign-in state, creating it on first use.
func (s *Store) Phone() *PhoneWorld {
	s.phoneOnce.Do(func() { s.phone = newPhoneWorld(s) })
	return s.phone
}

func newPhoneWorld(s *Store) *PhoneWorld {
	codes := otp.NewMemory()
	fake := &sms.Fake{}
	svc, err := otp.New(otp.Config{
		Store:    codes,
		Keys:     otp.StaticKeys{ID: "memory", Key: []byte("memory-world-phone-code-key-32b!")},
		Provider: func(sms.Config) (sms.Provider, error) { return fake, nil },
		Now:      func() time.Time { return s.clock() },
	})
	if err != nil {
		panic(err)
	}
	return &PhoneWorld{Codes: codes, SMS: fake, Service: svc, config: map[string]gen.PhoneSignIn{},
		abuse: map[string]gen.PhoneAbuseControls{}, zones: map[string]string{}, zoneAt: map[string]time.Time{}}
}

func (s *Store) siteKnown(siteID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, site := range s.world.Sites {
		if site.Id == siteID {
			return true
		}
	}
	return false
}

func (s *Store) PhoneSignIn(_ context.Context, siteID string) (gen.PhoneSignIn, error) {
	if !s.siteKnown(siteID) {
		return gen.PhoneSignIn{}, api.ErrNotFound
	}
	w := s.Phone()
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.config[siteID]
	if !ok {
		return gen.PhoneSignIn{SiteId: siteID}, nil
	}
	p.SentToday = w.Codes.SentOn(siteID, w.Service.DayOf(w.location(siteID), s.clock()))
	return p, nil
}

func (s *Store) SetPhoneSignIn(ctx context.Context, siteID string, in gen.PhoneSignInUpdate) (gen.PhoneSignIn, error) {
	if !s.siteKnown(siteID) {
		return gen.PhoneSignIn{}, api.ErrNotFound
	}
	w := s.Phone()
	w.mu.Lock()
	_, exists := w.config[siteID]
	if !exists && (in.ApiKey == nil || in.SecretKey == nil) {
		w.mu.Unlock()
		return gen.PhoneSignIn{}, api.ErrKeysRequired
	}
	provider := in.Provider
	p := gen.PhoneSignIn{
		SiteId: siteID, Provider: &provider, CredentialsSet: true,
		DailyLimit: &in.DailyLimit, SessionMinutes: &in.SessionMinutes, MessageTemplate: &in.MessageTemplate,
	}
	switch in.Provider {
	case gen.EsmsSms:
		p.Brandname = in.Brandname
	case gen.EsmsZalo:
		p.ZaloOaId, p.ZaloTemplateId = in.ZaloOaId, in.ZaloTemplateId
	}
	if in.Sandbox != nil {
		p.Sandbox = *in.Sandbox
	}
	w.config[siteID] = p
	w.sync(siteID)
	w.mu.Unlock()
	return s.PhoneSignIn(ctx, siteID)
}

// sync rebuilds what the code store reads for one site from the four settings
// that shape it — the provider, the limits, the deployment cap and the calendar —
// so a write to any of them is in force on the next send. The caller holds w.mu.
func (w *PhoneWorld) sync(siteID string) {
	p, ok := w.config[siteID]
	if !ok {
		return
	}
	set := otp.Settings{
		Provider: string(*p.Provider), DailyLimit: *p.DailyLimit, SessionMinutes: *p.SessionMinutes,
		MessageTemplate: *p.MessageTemplate, Sandbox: p.Sandbox,
		GlobalDailyLimit: w.cap.DailyLimit, Location: w.location(siteID),
	}
	if c, ok := w.abuse[siteID]; ok {
		l := otp.Limits{
			PerNumberHour: c.Limits.PerNumberPerHour, PerNumberDay: c.Limits.PerNumberPerDay,
			PerDeviceHour: c.Limits.PerDevicePerHour, PerAddressHour: c.Limits.PerAddressPerHour,
			MaxAttempts: c.Limits.MaxAttempts, CodeLifetime: time.Duration(c.Limits.CodeMinutes) * time.Minute,
		}
		set.Limits, set.MobilePrefixes = &l, c.MobilePrefixes
	}
	w.Codes.Configure(siteID, set)
}

// location is a site's zone in this world, or nil — the service's clock — for a
// site nobody set one for. The world is a test double: an unset zone keeps the
// calendar every existing test was written against.
func (w *PhoneWorld) location(siteID string) *time.Location {
	if z, ok := w.zones[siteID]; ok {
		if loc, err := time.LoadLocation(z); err == nil {
			return loc
		}
	}
	return nil
}

func (s *Store) PhoneAbuseControls(_ context.Context, siteID string) (gen.PhoneAbuseControls, error) {
	if !s.siteKnown(siteID) {
		return gen.PhoneAbuseControls{}, api.ErrNotFound
	}
	w := s.Phone()
	w.mu.Lock()
	defer w.mu.Unlock()
	if c, ok := w.abuse[siteID]; ok {
		return c, nil
	}
	return gen.PhoneAbuseControls{
		SiteId: siteID, Limits: api.PhoneLimits(otp.DefaultLimits),
		MobilePrefixes: append([]string(nil), otp.MobilePrefixes...), Country: gen.PhoneAbuseControlsCountryVN,
	}, nil
}

func (s *Store) SetPhoneAbuseControls(_ context.Context, siteID string, in gen.PhoneAbuseControlsUpdate) (gen.PhoneAbuseControls, error) {
	if !s.siteKnown(siteID) {
		return gen.PhoneAbuseControls{}, api.ErrNotFound
	}
	w := s.Phone()
	w.mu.Lock()
	defer w.mu.Unlock()
	at := s.clock()
	c := gen.PhoneAbuseControls{
		SiteId: siteID, Country: gen.PhoneAbuseControlsCountryVN, Customised: true, UpdatedAt: &at,
		MobilePrefixes: append([]string(nil), in.MobilePrefixes...),
		Limits: gen.PhoneSignInLimits{
			PerNumberPerHour: in.Limits.PerNumberPerHour, PerNumberPerDay: in.Limits.PerNumberPerDay,
			PerDevicePerHour: in.Limits.PerDevicePerHour, PerAddressPerHour: in.Limits.PerAddressPerHour,
			CodeMinutes: in.Limits.CodeMinutes, MaxAttempts: in.Limits.MaxAttempts,
		},
	}
	w.abuse[siteID] = c
	w.sync(siteID)
	return c, nil
}

func (s *Store) PhoneSpendCap(context.Context) (gen.PhoneSpendCap, error) {
	w := s.Phone()
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cap, nil
}

func (s *Store) SetPhoneSpendCap(_ context.Context, dailyLimit *int) (gen.PhoneSpendCap, error) {
	w := s.Phone()
	w.mu.Lock()
	defer w.mu.Unlock()
	at := s.clock()
	w.cap = gen.PhoneSpendCap{DailyLimit: dailyLimit, UpdatedAt: &at}
	for id := range w.config {
		w.sync(id)
	}
	return w.cap, nil
}

func (s *Store) SendPhoneTestMessage(ctx context.Context, siteID, phone string) (gen.PhoneTestMessageResult, error) {
	if !s.siteKnown(siteID) {
		return gen.PhoneTestMessageResult{}, api.ErrNotFound
	}
	w := s.Phone()
	res := w.Service.SendTest(ctx, siteID, phone)
	w.mu.Lock()
	sent := w.Codes.SentOn(siteID, w.Service.DayOf(w.location(siteID), s.clock()))
	w.mu.Unlock()
	out := gen.PhoneTestMessageResult{Outcome: gen.PhoneTestMessageOutcome(res.Outcome), SentToday: sent}
	if res.ProviderCode != "" {
		code := res.ProviderCode
		out.ProviderCode = &code
	}
	return out, nil
}

// ── the site's calendar ──────────────────────────────────────────────────────

var _ api.SiteTimeZoneStore = (*Store)(nil)

func (s *Store) SiteTimeZone(_ context.Context, siteID string) (gen.SiteTimeZone, error) {
	if !s.siteKnown(siteID) {
		return gen.SiteTimeZone{}, api.ErrNotFound
	}
	w := s.Phone()
	w.mu.Lock()
	defer w.mu.Unlock()
	z, ok := w.zones[siteID]
	if !ok {
		z = api.DefaultTimeZone
	}
	tz := gen.SiteTimeZone{SiteId: siteID, TimeZone: z}
	if at, ok := w.zoneAt[siteID]; ok {
		tz.UpdatedAt = &at
	}
	return tz, nil
}

func (s *Store) SetSiteTimeZone(ctx context.Context, siteID, zone string) (gen.SiteTimeZone, error) {
	if !s.siteKnown(siteID) {
		return gen.SiteTimeZone{}, api.ErrNotFound
	}
	w := s.Phone()
	w.mu.Lock()
	// The change time moves only when the zone does, as `store.SiteRepo.SetTimeZone`.
	if before, ok := w.zones[siteID]; (ok && before != zone) || (!ok && zone != api.DefaultTimeZone) {
		w.zoneAt[siteID] = s.clock()
	}
	w.zones[siteID] = zone
	w.sync(siteID)
	w.mu.Unlock()
	return s.SiteTimeZone(ctx, siteID)
}

// SiteLocation is nil for a site with no zone of its own: the server's clock
// applies, which is the calendar this world's tests were written against.
func (s *Store) SiteLocation(_ context.Context, siteID string) (*time.Location, error) {
	if !s.siteKnown(siteID) {
		return nil, api.ErrNotFound
	}
	w := s.Phone()
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.location(siteID), nil
}

// phoneCodes is the guest seam over the same service.
func (s *Store) phoneCodes() portal.PhoneCodes { return otp.PortalCodes(s.Phone().Service) }
