package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// The guest-data port, in memory. **A fake, and labelled one**:
// the real records live in `internal/store/privacy_guestdata.go` and are proven
// against PostgreSQL there. This exists so the handlers — the consent rule, the
// portal's refusals, the audit entries — can be driven without a database.

type memGuestData struct {
	settings map[string]gen.GuestDataSettings
	// texts is every published text, by site then version.
	texts    map[string]map[string]memText
	details  []api.GuestDetails
	consents []gen.MarketingConsent
	seq      int
}

type memText struct{ lang, body string }

func memVersion(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

var _ api.GuestDataStore = (*Store)(nil)

func (s *Store) GuestDataSettings(_ context.Context, siteID string) (gen.GuestDataSettings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if set, ok := s.guestData.settings[siteID]; ok {
		return set, nil
	}
	return gen.GuestDataSettings{Fields: []gen.GuestDataFieldSetting{}}, nil
}

func (s *Store) SetGuestDataSettings(_ context.Context, siteID string, in gen.GuestDataSettingsUpdate) (gen.GuestDataSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := &s.guestData
	if g.settings == nil {
		g.settings, g.texts = map[string]gen.GuestDataSettings{}, map[string]map[string]memText{}
	}
	if g.texts[siteID] == nil {
		g.texts[siteID] = map[string]memText{}
	}
	out := gen.GuestDataSettings{Fields: append([]gen.GuestDataFieldSetting{}, in.Fields...)}
	out.MarketingConsent.Enabled = in.MarketingConsent.Enabled
	publish := func(lang string, raw *string, text, version **string) {
		if raw == nil || strings.TrimSpace(*raw) == "" {
			return
		}
		body := strings.TrimSpace(*raw)
		v := memVersion(body)
		*text, *version = &body, &v
		if _, ok := g.texts[siteID][v]; !ok {
			g.texts[siteID][v] = memText{lang: lang, body: body}
		}
	}
	mc := &out.MarketingConsent
	publish("en", in.MarketingConsent.Text.En, &mc.Text.En, &mc.Versions.En)
	publish("vi", in.MarketingConsent.Text.Vi, &mc.Text.Vi, &mc.Versions.Vi)
	g.settings[siteID] = out
	return out, nil
}

func (s *Store) RecordGuestDetails(_ context.Context, d api.GuestDetails) error {
	if d.Name == nil && d.Phone == nil && d.Email == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guestData.details = append(s.guestData.details, d)
	return nil
}

// GuestDetails is what was recorded, for a test to read.
func (s *Store) GuestDetails() []api.GuestDetails {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]api.GuestDetails{}, s.guestData.details...)
}

func (s *Store) RecordMarketingConsent(_ context.Context, c api.ConsentGiven) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := &s.guestData
	text, ok := g.texts[c.SiteID][c.Version]
	if !ok {
		return api.ErrUnknownConsentVersion
	}
	g.seq++
	g.consents = append(g.consents, gen.MarketingConsent{
		Id: fmt.Sprintf("mc-%08d", g.seq), SiteId: c.SiteID, ClientMac: c.ClientMAC,
		ConsentedAt: s.now().UTC(), State: gen.Active, Lang: gen.Language(text.lang),
		ConsentVersion: c.Version, ConsentText: text.body,
		Name: c.Name, Phone: c.Phone, Email: c.Email,
	})
	return nil
}

func (s *Store) MarketingConsents(_ context.Context, siteID string, state *gen.MarketingConsentState, w api.Window) (api.Page[gen.MarketingConsent], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var items []gen.MarketingConsent
	for i := len(s.guestData.consents) - 1; i >= 0; i-- {
		c := s.guestData.consents[i]
		if c.SiteId == siteID && (state == nil || c.State == *state) {
			items = append(items, c)
		}
	}
	return slicePage(items, w), nil
}

// withdrawLocked is the one rule both withdrawals share: the contact goes with it.
func (s *Store) withdrawLocked(i int, by gen.ConsentWithdrawnBy) {
	c := &s.guestData.consents[i]
	if c.State == gen.Withdrawn {
		return
	}
	now := s.now().UTC()
	c.State, c.WithdrawnAt, c.WithdrawnBy = gen.Withdrawn, &now, &by
	c.Name, c.Phone, c.Email = nil, nil, nil
}

func (s *Store) WithdrawMarketingConsent(_ context.Context, siteID, consentID string) (gen.MarketingConsent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.guestData.consents {
		if c.SiteId == siteID && c.Id == consentID {
			s.withdrawLocked(i, gen.ConsentWithdrawnByOperator)
			return s.guestData.consents[i], nil
		}
	}
	return gen.MarketingConsent{}, api.ErrNotFound
}

func (s *Store) WithdrawDeviceConsents(_ context.Context, siteID, mac string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want, _ := macHex(mac)
	n := 0
	for i, c := range s.guestData.consents {
		if h, _ := macHex(c.ClientMac); c.SiteId == siteID && h == want && c.State == gen.Active {
			s.withdrawLocked(i, gen.ConsentWithdrawnByGuest)
			n++
		}
	}
	return n, nil
}

func (s *Store) ConsentsBetween(_ context.Context, siteID string, r api.Range, visit func(gen.MarketingConsent) error) error {
	s.mu.RLock()
	var rows []gen.MarketingConsent
	for _, c := range s.guestData.consents {
		if c.SiteId == siteID && (r.Contains(c.ConsentedAt) || (c.WithdrawnAt != nil && r.Contains(*c.WithdrawnAt))) {
			rows = append(rows, c)
		}
	}
	s.mu.RUnlock()
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ConsentedAt.Before(rows[j].ConsentedAt) })
	for _, c := range rows {
		if err := visit(c); err != nil {
			return err
		}
	}
	return nil
}

// eraseMAC removes one device's consents; the caller holds the lock.
func (g *memGuestData) eraseMAC(hexMAC string) int {
	kept := g.consents[:0]
	n := 0
	for _, c := range g.consents {
		if h, _ := macHex(c.ClientMac); h == hexMAC {
			n++
			continue
		}
		kept = append(kept, c)
	}
	g.consents = kept
	return n
}

// erasePhone removes the details and the consents that give a number.
func (g *memGuestData) erasePhone(isNumber func(*string) bool) (details, consents int) {
	keptD := g.details[:0]
	for _, d := range g.details {
		if isNumber(d.Phone) {
			details++
			continue
		}
		keptD = append(keptD, d)
	}
	g.details = keptD
	keptC := g.consents[:0]
	for _, c := range g.consents {
		if isNumber(c.Phone) {
			consents++
			continue
		}
		keptC = append(keptC, c)
	}
	g.consents = keptC
	return details, consents
}

// eraseEmail removes the details and the consents that give an address.
func (g *memGuestData) eraseEmail(is func(*string) bool) (details, consents int) {
	keptD := g.details[:0]
	for _, d := range g.details {
		if is(d.Email) {
			details++
			continue
		}
		keptD = append(keptD, d)
	}
	g.details = keptD
	keptC := g.consents[:0]
	for _, c := range g.consents {
		if is(c.Email) {
			consents++
			continue
		}
		keptC = append(keptC, c)
	}
	g.consents = keptC
	return details, consents
}

func (s *Store) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}
