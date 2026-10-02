package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/portal"
)

// ReportReader over the world.
//
// The rules are the contract's and `internal/store`'s reports.go states them in
// SQL; this is the same four rules over slices, and the differential harness is
// what holds the two together. A rule restated here that disagreed with the store
// would be a finding for `make diff-api`, not a second answer.

var _ api.ReportReader = (*Store)(nil)

// SessionsStarted is `startedAt` in the range, oldest first, ties by id.
func (s *Store) SessionsStarted(_ context.Context, siteID string, r api.Range, visit func(gen.Session) error) error {
	s.mu.RLock()
	var rows []gen.Session
	for _, sess := range s.world.Sessions {
		if sess.SiteId == siteID && r.Contains(sess.StartedAt) {
			rows = append(rows, sess)
		}
	}
	s.mu.RUnlock()
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].StartedAt.Equal(rows[j].StartedAt) {
			return rows[i].StartedAt.Before(rows[j].StartedAt)
		}
		return rows[i].Id < rows[j].Id
	})
	for _, row := range rows {
		if err := visit(row); err != nil {
			return err
		}
	}
	return nil
}

// VoucherActivity is created or first used in the range, by creation, oldest first.
func (s *Store) VoucherActivity(_ context.Context, siteID string, r api.Range, visit func(gen.Voucher) error) error {
	s.mu.RLock()
	inSite := s.batchesIn(siteID)
	var rows []gen.Voucher
	for _, v := range s.world.Vouchers {
		if !inSite[v.BatchId] {
			continue
		}
		if within(r, v.CreatedAt) || within(r, v.FirstUsedAt) {
			rows = append(rows, v)
		}
	}
	s.mu.RUnlock()
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := at(rows[i].CreatedAt), at(rows[j].CreatedAt)
		if !a.Equal(b) {
			return a.Before(b)
		}
		return rows[i].Id < rows[j].Id
	})
	for _, row := range rows {
		if err := visit(row); err != nil {
			return err
		}
	}
	return nil
}

// GuestGrants is `grantedAt` in the range, oldest first.
//
// The grants are the portal fake's, which is where this world records them: a
// grant is made by driving the guest surface, never seeded.
func (s *Store) GuestGrants(_ context.Context, siteID string, r api.Range, visit func(portal.Grant) error) error {
	rows := s.grantsIn(siteID, r)
	for _, g := range rows {
		if err := visit(g); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) grantsIn(siteID string, r api.Range) []portal.Grant {
	if s.PortalStore == nil {
		return nil
	}
	var rows []portal.Grant
	for _, g := range s.PortalStore.Grants() {
		if g.SiteID == siteID && r.Contains(g.GrantedAt) {
			rows = append(rows, g)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].GrantedAt.Equal(rows[j].GrantedAt) {
			return rows[i].GrantedAt.Before(rows[j].GrantedAt)
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

// Trends counts each bucket by the contract's four rules.
func (s *Store) Trends(_ context.Context, siteID string, buckets []api.Range) ([]gen.TrendBucket, error) {
	now := s.clock()
	s.mu.RLock()
	inSite := s.batchesIn(siteID)
	sessions := append([]gen.Session{}, s.world.Sessions...)
	vouchers := append([]gen.Voucher{}, s.world.Vouchers...)
	s.mu.RUnlock()

	out := make([]gen.TrendBucket, 0, len(buckets))
	for _, b := range buckets {
		macs := map[string]bool{}
		var bytes int64
		wrapped := false
		var oldest *time.Time
		for _, sess := range sessions {
			if sess.SiteId != siteID || !b.Contains(sess.StartedAt) {
				continue
			}
			if m := macKey(sess.CallingStationId); m != "" {
				macs[m] = true
			}
			if sess.InputOctetsTotal == nil && sess.OutputOctetsTotal == nil {
				continue
			}
			bytes += int64(deref(sess.InputOctetsTotal)) + int64(deref(sess.OutputOctetsTotal))
			wrapped = wrapped || sess.CountersWrapped
			if sess.EndedAt == nil {
				heard := sess.StartedAt
				if sess.LastUpdateAt != nil {
					heard = *sess.LastUpdateAt
				}
				if oldest == nil || heard.Before(*oldest) {
					oldest = &heard
				}
			}
		}
		for _, g := range s.grantsIn(siteID, b) {
			if m := macKey(g.ClientMAC); m != "" {
				macs[m] = true
			}
		}
		redeemed := 0
		for _, v := range vouchers {
			if inSite[v.BatchId] && within(b, v.FirstUsedAt) {
				redeemed++
			}
		}
		out = append(out, gen.TrendBucket{
			GuestDevices:     len(macs),
			VouchersRedeemed: redeemed,
			Bytes:            bytes,
			BytesLowerBound:  wrapped,
			BytesFreshness:   trendFreshness(oldest, now),
		})
	}
	return out, nil
}

// trendFreshness is `Metric`'s three clauses over the open rows.
func trendFreshness(oldest *time.Time, now time.Time) gen.Freshness {
	if oldest == nil {
		return gen.Freshness{State: gen.FreshnessStateLive, SampledAt: ptr(now)}
	}
	state := gen.FreshnessStateSampled
	if now.Sub(*oldest) > 2*300*time.Second {
		state = gen.FreshnessStateStale
	}
	return gen.Freshness{State: state, SampledAt: ptr(*oldest), IntervalSeconds: ptr(300)}
}

// macKey is a MAC's twelve hex digits in lower case, or "" for anything that is
// not one. Calling-Station-Id has no canonical spelling, and a device counted
// twice because two tables spelled it differently is two guests who are one.
func macKey(mac string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(mac) {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			b.WriteRune(c)
		}
	}
	if b.Len() != 12 {
		return ""
	}
	return b.String()
}

func (s *Store) batchesIn(siteID string) map[string]bool {
	in := map[string]bool{}
	for _, b := range s.world.Batches {
		if b.SiteId == siteID {
			in[b.Id] = true
		}
	}
	return in
}

func within(r api.Range, t *time.Time) bool { return t != nil && r.Contains(*t) }

func at(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func deref(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}
