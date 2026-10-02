package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/redact"
)

// The privacy port, in memory. **A fake, and labelled one**: the real retention and
// erasure live in `internal/store` (privacy.go, retention.go) and are proven against
// PostgreSQL there. This exists so the API's handlers -- validation, gating, audit --
// can be driven without a database, and so the differential harness has a memory
// mode to compare against the mock.
//
// The category table is a copy of `store.RetentionCategories` and the erased-table
// lists a copy of its erasers, because this package does not import the store. The
// differential harness compares this fake, the adapter and `web/mock` operation by
// operation, which is what catches the copies drifting.

type memCategory struct {
	id            gen.RetentionCategoryId
	def, min, max int
	tables        []string
}

var memCategories = []memCategory{
	{gen.Accounting, 90, 1, 3650, []string{"radacct"}},
	{gen.AuthAttempts, 30, 1, 3650, []string{"radpostauth", "authorization_events"}},
	{gen.PortalGrants, 90, 1, 3650, []string{"portal_grants"}},
	{gen.Audit, 730, 30, 3650, []string{"audit_log"}},
	{gen.GuestAccounts, 0, 0, 3650, []string{"accounts"}},
	{gen.MarketingConsents, 730, 30, 3650, []string{"marketing_consents"}},
}

var memErasedTables = map[gen.DataSubjectKind][]string{
	gen.DataSubjectKindMac: {"authorization_events", "marketing_consents", "phone_codes", "portal_addresses",
		"portal_grants", "portal_sessions", "radacct", "radpostauth"},
	gen.DataSubjectKindAccount: {"accounts", "authorization_events", "marketing_consents", "portal_grants", "portal_sessions", "radacct", "radpostauth"},
	gen.DataSubjectKindPhone: {"authorization_events", "guest_details", "marketing_consents", "phone_codes",
		"portal_addresses", "portal_grants", "portal_sessions", "radacct", "radpostauth"},
	gen.DataSubjectKindEmail: {"guest_details", "marketing_consents"},
}

// phoneForms is `store.PhoneSubject` for this world, which does not import the
// store: the number's digit strings (international and, for a Vietnamese number,
// national) and the exact spellings a username may hold. `TestPhoneFormsAreTheStores`
// holds the two to the same vectors.
func phoneForms(raw string) (digits, spellings []string, ok bool) {
	raw = strings.TrimSpace(raw)
	var d []byte
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c >= '0' && c <= '9':
			d = append(d, c)
		case c == '+' && i == 0:
		case c == ' ' || c == '.' || c == '-' || c == '(' || c == ')':
		default:
			return nil, nil, false
		}
	}
	if len(d) < 8 || len(d) > 15 {
		return nil, nil, false
	}
	intl, national := string(d), ""
	switch {
	case len(d) == 10 && d[0] == '0':
		intl, national = "84"+string(d[1:]), string(d)
	case len(d) == 11 && strings.HasPrefix(intl, "84"):
		national = "0" + intl[2:]
	}
	digits, spellings = []string{intl}, []string{"+" + intl, intl}
	if national != "" {
		digits, spellings = append(digits, national), append(spellings, national)
	}
	return digits, spellings, true
}

func digitsOf(v *string) string {
	if v == nil {
		return ""
	}
	var b strings.Builder
	for _, r := range *v {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var _ api.PrivacyStore = (*Store)(nil)

func (s *Store) RetentionPolicy(context.Context) (gen.RetentionPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.retentionPolicyLocked(), nil
}

func (s *Store) retentionPolicyLocked() gen.RetentionPolicy {
	out := gen.RetentionPolicy{Categories: []gen.RetentionCategoryPolicy{}}
	for _, c := range memCategories {
		days, set := s.retention[c.id]
		if !set {
			days = c.def
		}
		out.Categories = append(out.Categories, gen.RetentionCategoryPolicy{
			Category: c.id, Days: days, DefaultDays: c.def, MinimumDays: c.min, MaximumDays: c.max,
			IsDefault: !set, Tables: append([]string{}, c.tables...),
		})
	}
	return out
}

func (s *Store) AmendRetentionPolicy(_ context.Context, changes []gen.RetentionPolicyChange, _ string,
	record func() (api.AuditEntry, error)) (gen.RetentionPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The entry first and the change only if it could be made: the store's
	// transaction, in the only order a fake without one can keep.
	e, err := record()
	if err != nil {
		return gen.RetentionPolicy{}, err
	}
	s.world.Audit = append([]gen.AuditEntry{e}, s.world.Audit...)
	if s.retention == nil {
		s.retention = map[gen.RetentionCategoryId]int{}
	}
	for _, ch := range changes {
		if ch.Days == nil {
			delete(s.retention, ch.Category)
			continue
		}
		s.retention[ch.Category] = *ch.Days
	}
	return s.retentionPolicyLocked(), nil
}

func (s *Store) EraseDataSubject(_ context.Context, want api.ErasureSubject,
	record func(gen.DataSubjectErasure) (api.AuditEntry, error)) (gen.DataSubjectErasure, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kind, mac, accountID := want.Kind, want.MAC, want.AccountID
	counts := map[string]int{}
	var masked string
	// names are what the audit log may call the subject by, for its
	// pseudonymisation below; canonical is what the pseudonym is computed over.
	var names []string
	var canonical string
	switch kind {
	case gen.DataSubjectKindEmail:
		email := strings.TrimSpace(want.Email)
		at := strings.LastIndexByte(email, '@')
		if len(email) > 254 || strings.ContainsAny(email, " \t\r\n") || strings.Count(email, "@") != 1 ||
			at < 1 || at == len(email)-1 || !strings.Contains(email[at+1:], ".") {
			return gen.DataSubjectErasure{}, api.ErrNotAnEmailAddress
		}
		masked, canonical, names = redact.Email(email), email, []string{email}
		is := func(v *string) bool { return v != nil && strings.EqualFold(*v, email) }
		counts["guest_details"], counts["marketing_consents"] = s.guestData.eraseEmail(is)
	case gen.DataSubjectKindPhone:
		digits, spellings, ok := phoneForms(want.Phone)
		if !ok {
			return gen.DataSubjectErasure{}, api.ErrNotAPhoneNumber
		}
		masked, canonical, names = redact.Phone(digits[0]), digits[0], spellings
		isNumber := func(v *string) bool { return v != nil && slices.Contains(digits, digitsOf(v)) }
		isName := func(v *string) bool { return v != nil && slices.Contains(spellings, *v) }
		counts["phone_codes"] += s.Phone().Codes.EraseWhere(func(_, phone string) bool { return isNumber(&phone) })
		counts["guest_details"], counts["marketing_consents"] = s.guestData.erasePhone(isNumber)
		kept := s.world.Sessions[:0]
		for _, x := range s.world.Sessions {
			if isName(x.Username) {
				counts["radacct"]++
				continue
			}
			kept = append(kept, x)
		}
		s.world.Sessions = kept
		keptA := s.world.Attempts[:0]
		for _, x := range s.world.Attempts {
			if isName(x.Username) {
				if x.Source == gen.Radius {
					counts["radpostauth"]++
				} else {
					counts["authorization_events"]++
				}
				continue
			}
			keptA = append(keptA, x)
		}
		s.world.Attempts = keptA
	case gen.DataSubjectKindMac:
		hex, ok := macHex(mac)
		if !ok {
			return gen.DataSubjectErasure{}, api.ErrNotAMACAddress
		}
		counts["marketing_consents"] += s.guestData.eraseMAC(hex)
		counts["phone_codes"] += s.Phone().Codes.EraseWhere(func(m, _ string) bool {
			h, _ := macHex(m)
			return h == hex
		})
		masked, canonical = redact.MAC(hex), hex
		kept := s.world.Sessions[:0]
		for _, x := range s.world.Sessions {
			if h, _ := macHex(x.CallingStationId); h == hex {
				counts["radacct"]++
				continue
			}
			kept = append(kept, x)
		}
		s.world.Sessions = kept
		keptA := s.world.Attempts[:0]
		for _, x := range s.world.Attempts {
			if x.CallingStationId != nil {
				if h, _ := macHex(*x.CallingStationId); h == hex {
					if x.Source == gen.Radius {
						counts["radpostauth"]++
					} else {
						counts["authorization_events"]++
					}
					continue
				}
			}
			keptA = append(keptA, x)
		}
		s.world.Attempts = keptA
	case gen.DataSubjectKindAccount:
		i := -1
		for j, a := range s.world.Accounts {
			if a.Id == accountID {
				i = j
			}
		}
		if i < 0 {
			return gen.DataSubjectErasure{}, api.ErrNotFound
		}
		names = append(names, s.world.Accounts[i].Username, accountID)
		s.world.Accounts = append(s.world.Accounts[:i], s.world.Accounts[i+1:]...)
		counts["accounts"] = 1
		masked, canonical = "account "+accountID, accountID
	}

	r := gen.DataSubjectErasure{At: time.Now().UTC(), Removed: []gen.ErasedTable{}}
	r.Subject.Kind, r.Subject.Masked = kind, masked
	tables := append([]string{}, memErasedTables[kind]...)
	sort.Strings(tables)
	for _, t := range tables {
		r.Removed = append(r.Removed, gen.ErasedTable{Table: t, Rows: counts[t]})
		r.Total += counts[t]
	}
	r.Pseudonymised = []gen.ErasedTable{{Table: "audit_log", Rows: s.pseudonymiseAuditLocked(kind, canonical, names)}}
	e, err := record(r)
	if err != nil {
		return gen.DataSubjectErasure{}, err
	}
	s.world.Audit = append([]gen.AuditEntry{e}, s.world.Audit...)
	return r, nil
}

// pseudonymiseAuditLocked is `foyer_audit_pseudonymise` (migration 00052) for this
// world: an entry's subject id or name that is the subject — a MAC in any spelling,
// or exactly one of names — becomes `pseudonym:` and sixteen hex digits. Unkeyed,
// because a fake has no KEK; the shape and the count are what the differential
// harness compares.
func (s *Store) pseudonymiseAuditLocked(kind gen.DataSubjectKind, canonical string, names []string) int {
	sum := sha256.Sum256([]byte(string(kind) + "\x00" + canonical))
	pseudonym := "pseudonym:" + hex.EncodeToString(sum[:8])
	is := func(v string) bool {
		if kind == gen.DataSubjectKindMac {
			h, ok := macHex(v)
			return ok && h == canonical
		}
		for _, n := range names {
			if strings.EqualFold(v, n) {
				return true
			}
		}
		return false
	}
	n := 0
	for i := range s.world.Audit {
		e := &s.world.Audit[i]
		if e.Subject == nil {
			continue
		}
		hit := false
		if is(e.Subject.Id) {
			e.Subject.Id, hit = pseudonym, true
		}
		if e.Subject.Name != nil && is(*e.Subject.Name) {
			p := pseudonym
			e.Subject.Name, hit = &p, true
		}
		if hit {
			n++
		}
	}
	return n
}

// macHex accepts the four spellings a NAS sends, as store.MACSubject does.
func macHex(raw string) (string, bool) {
	s := strings.ToLower(raw)
	var sepAt func(i int) bool
	var sep byte
	switch len(s) {
	case 12:
		sepAt = func(int) bool { return false }
	case 17:
		sep = s[2]
		if sep != ':' && sep != '-' {
			return "", false
		}
		sepAt = func(i int) bool { return i%3 == 2 }
	case 14:
		sep = '.'
		sepAt = func(i int) bool { return i == 4 || i == 9 }
	default:
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if sepAt(i) {
			if c != sep {
				return "", false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", false
		}
		b.WriteByte(c)
	}
	return b.String(), true
}
