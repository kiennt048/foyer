package apistore

import (
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// The two ends of `orphanFreshness`, and the empty one is the reason this file
// exists — `TestTheAdapterAnswersTheContractFromRealRows` runs against a world that
// always holds an orphan, so clause 1 is unreachable there.
//
// `Metric`'s first clause is *nothing counted → `live`,
// dated now*, and it applies to this figure unchanged: there is no sample behind a
// figure with no rows in it, and "no session is unaccounted for" is what the server
// knows at that instant rather than something it was told forty days ago.
//
// The failure it locks is specific and is the tempting shape here:
// carrying the *online* figure's timestamp, or `now`, into an empty orphan count,
// so a site with nothing wrong reports a sample time for a set with no rows in it.
func TestOrphanFreshnessAtBothEnds(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	oldest := now.Add(-40 * 24 * time.Hour)
	online := now.Add(-30 * time.Second)

	t.Run("no orphans is live and dated now", func(t *testing.T) {
		f := orphanFreshness(store.SessionCounts{Online: 12, OldestOnlineUpdate: &online}, now)
		if f.State != gen.FreshnessStateLive {
			t.Errorf("state = %q, want live: a figure with no rows in it has no sample behind it", f.State)
		}
		if f.SampledAt == nil || !f.SampledAt.Equal(now) {
			t.Errorf("sampledAt = %v, want %v: zero orphans is what the server knows at this "+
				"instant, and `live` with no date names a claim and then declines to date it", f.SampledAt, now)
		}
		// The online count's timestamp must not leak in here. The two figures
		// share a struct, and this is the branch where the wrong one is invisible.
		if f.SampledAt != nil && f.SampledAt.Equal(online) {
			t.Error("the empty orphan count is dated with the online figure's sample")
		}
	})

	t.Run("orphans carry the oldest of their own rows, with no interval", func(t *testing.T) {
		f := orphanFreshness(store.SessionCounts{
			Online: 12, Orphaned: 3, OldestOnlineUpdate: &online, OldestOrphanUpdate: &oldest,
		}, now)
		if f.State != gen.FreshnessStateStale {
			t.Errorf("state = %q, want stale", f.State)
		}
		if f.SampledAt == nil || !f.SampledAt.Equal(oldest) {
			t.Errorf("sampledAt = %v, want the oldest counted orphan %v", f.SampledAt, oldest)
		}
		// See `orphanFreshness`: membership is decided against each row's own
		// interval, so there is no one cadence this figure could declare.
		if f.IntervalSeconds != nil {
			t.Errorf("intervalSeconds = %d, want nil", *f.IntervalSeconds)
		}
	})
}
