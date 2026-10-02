package otp

import (
	"context"
	"sync"
	"time"
)

// Memory is the in-process Store, for tests and for `internal/api/memory`. It keeps
// the same guarantees as the SQL one, under one mutex rather than one row lock.
type Memory struct {
	mu       sync.Mutex
	settings map[string]Settings
	codes    []*memCode
	sends    map[string]int // site|day -> count
	totals   map[string]int // day -> count across every site
	// FailCeiling makes the ceiling unreadable, to prove it fails closed.
	FailCeiling bool
}

type memCode struct {
	Issue
	attempts int
	consumed *time.Time
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{settings: map[string]Settings{}, sends: map[string]int{}, totals: map[string]int{}}
}

// Configure stores a site's settings.
func (m *Memory) Configure(siteID string, s Settings) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings[siteID] = s
}

// SentOn reports the ceiling's count for one site and day.
func (m *Memory) SentOn(siteID, day string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sends[siteID+"|"+day]
}

// count charges one message against both ceilings. The caller holds m.mu.
func (m *Memory) count(siteID, day string, daily int, global *int) error {
	if m.FailCeiling {
		return ErrCeiling
	}
	if m.totals == nil {
		m.totals = map[string]int{}
	}
	key := siteID + "|" + day
	if m.sends[key] >= daily || (global != nil && m.totals[day] >= *global) {
		return ErrCeiling
	}
	m.sends[key]++
	m.totals[day]++
	return nil
}

// Count is Store's Count.
func (m *Memory) Count(_ context.Context, c CountedSend) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count(c.SiteID, c.Day, c.DailyLimit, c.GlobalLimit)
}

// EraseWhere deletes every code whose device or number matches, and answers how
// many went — `internal/api/memory`'s half of the `phone_codes` eraser.
func (m *Memory) EraseWhere(match func(mac, phone string) bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.codes[:0]
	n := 0
	for _, c := range m.codes {
		if match(c.ClientMAC, c.Phone) {
			n++
			continue
		}
		kept = append(kept, c)
	}
	m.codes = kept
	return n
}

// Stored returns the stored row for id, for tests that assert what is at rest.
func (m *Memory) Stored(id string) (Issue, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.codes {
		if c.ID == id {
			return c.Issue, true
		}
	}
	return Issue{}, false
}

func (m *Memory) PhoneSettings(_ context.Context, siteID string) (Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.settings[siteID]
	if !ok {
		return Settings{}, ErrNotOffered
	}
	return s, nil
}

func (m *Memory) Issue(_ context.Context, in Issue) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var hour, day int
	for _, c := range m.codes {
		if c.SiteID != in.SiteID || c.Phone != in.Phone {
			continue
		}
		if in.At.Sub(c.At) < time.Hour {
			hour++
		}
		if in.At.Sub(c.At) < 24*time.Hour {
			day++
		}
	}
	if hour >= in.PerNumberHour || day >= in.PerNumberDay {
		return ErrLimited
	}
	if err := m.count(in.SiteID, in.Day, in.DailyLimit, in.GlobalLimit); err != nil {
		return err
	}
	for _, c := range m.codes {
		if c.SessionTokenHash == in.SessionTokenHash && c.consumed == nil {
			t := in.At
			c.consumed = &t
		}
	}
	m.codes = append(m.codes, &memCode{Issue: in})
	return nil
}

func (m *Memory) Attempt(_ context.Context, sessionTokenHash string, at time.Time) (Attempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var newest *memCode
	for _, c := range m.codes {
		if c.SessionTokenHash != sessionTokenHash || c.consumed != nil || !c.ExpiresAt.After(at) {
			continue
		}
		if newest == nil || c.At.After(newest.At) {
			newest = c
		}
	}
	if newest == nil || newest.attempts >= newest.MaxAttempts {
		return Attempt{}, ErrCodeExpired
	}
	newest.attempts++
	return Attempt{
		ID: newest.ID, Phone: newest.Phone, MAC: newest.MAC, KeyID: newest.KeyID,
		Attempts: newest.attempts, MaxAttempts: newest.MaxAttempts,
	}, nil
}

func (m *Memory) Consume(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.codes {
		if c.ID == id && c.consumed == nil {
			t := at
			c.consumed = &t
			return nil
		}
	}
	return ErrCodeExpired
}

func (m *Memory) Retire(ctx context.Context, id string, at time.Time) error {
	if err := m.Consume(ctx, id, at); err != nil && err != ErrCodeExpired {
		return err
	}
	return nil
}

// StaticKeys is a fixed key, for tests.
type StaticKeys struct {
	ID  string
	Key []byte
}

func (k StaticKeys) PhoneCodeKey() (string, []byte, error) { return k.ID, k.Key, nil }
func (k StaticKeys) PhoneCodeKeyByID(id string) ([]byte, error) {
	if id != k.ID {
		return nil, ErrCodeExpired
	}
	return k.Key, nil
}
