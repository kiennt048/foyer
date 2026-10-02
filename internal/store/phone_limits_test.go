package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

// The per-number limits across time, in SQL. A test that issues every code at the
// same instant cannot tell a one-hour window from a one-day one, and never
// reaches the day limit at all: without this one, widening `interval '1 hour'` to
// a day, or dropping `day >= PerNumberDay`, would leave every test green.
func TestPhonePerNumberLimitsAcrossTheHourAndTheDay(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	t0 := time.Now().UTC().Truncate(time.Second).Add(-26 * time.Hour)
	const phone = "+84912000444"

	n := 0
	send := func(at time.Time) error {
		n++
		in := issue(site.ID, uniq("sess"), phone, at, n)
		in.DailyLimit = 1000
		return st.Phone().Issue(ctx, in)
	}

	for i := 0; i < 3; i++ {
		fatalf(t, send(t0.Add(time.Duration(i)*time.Minute)), "hour one")
	}
	if err := send(t0.Add(10 * time.Minute)); !errors.Is(err, store.ErrPhoneLimited) {
		t.Fatalf("fourth code in one hour: %v", err)
	}
	// Past the hour, the hour window has room: two more reach five today.
	later := t0.Add(time.Hour + 5*time.Minute)
	for i := 0; i < 2; i++ {
		if err := send(later.Add(time.Duration(i) * time.Minute)); err != nil {
			t.Fatalf("code %d in hour two (the hour window is not an hour?): %v", i, err)
		}
	}
	// The sixth of the day is refused by the day limit alone.
	if err := send(later.Add(10 * time.Minute)); !errors.Is(err, store.ErrPhoneLimited) {
		t.Fatalf("sixth code to one number in a day: %v", err)
	}
	// A day after the first three, the day window has room again.
	if err := send(t0.Add(24*time.Hour + 10*time.Minute)); err != nil {
		t.Fatalf("a day later: %v", err)
	}
}
