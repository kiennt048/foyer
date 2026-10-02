package apistore

import (
	"context"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/portal"
	"github.com/kiennt048/foyer/internal/store"
)

// ReportReader — the three exports and the trends.
//
// Every row goes through the converter the list operations use — `sessionOut`,
// `voucherOut`, `grantOut` — so an exported session and a listed one cannot
// disagree about a state, a freshness or a wrapped counter. Nothing is buffered
// here: each repository scan hands one row to one visitor.

func (a *Adapter) SessionsStarted(ctx context.Context, siteID string, r api.Range, visit func(gen.Session) error) error {
	return out(a.st.Accounting().SessionsStartedBetween(ctx, siteID, r.From, r.To,
		func(s store.Session) error { return visit(sessionOut(s)) }))
}

func (a *Adapter) VoucherActivity(ctx context.Context, siteID string, r api.Range, visit func(gen.Voucher) error) error {
	return out(a.st.Vouchers().VouchersActiveBetween(ctx, siteID, r.From, r.To,
		func(v store.Voucher) error { return visit(voucherOut(v)) }))
}

func (a *Adapter) GuestGrants(ctx context.Context, siteID string, r api.Range, visit func(portal.Grant) error) error {
	return out(a.st.Grants().GrantsBetween(ctx, siteID, r.From, r.To,
		func(g store.GrantWithAccount) error {
			p := grantOut(g.PortalGrant)
			if g.AccountUsername != nil {
				p.Username = *g.AccountUsername
			}
			return visit(p)
		}))
}

// Trends answers the store's counts with `Metric`'s freshness rule over the open
// rows — the rule `onlineFreshness` applies to `clientsOnline`, with the same
// 300-second floor, so the verdict is reproducible from the two numbers rendered.
func (a *Adapter) Trends(ctx context.Context, siteID string, buckets []api.Range) ([]gen.TrendBucket, error) {
	starts := make([]time.Time, len(buckets))
	ends := make([]time.Time, len(buckets))
	for i, b := range buckets {
		starts[i], ends[i] = b.From, b.To
	}
	counted, err := a.st.Accounting().Trends(ctx, siteID, starts, ends)
	if err != nil {
		return nil, out(err)
	}
	now := a.now()
	res := make([]gen.TrendBucket, len(counted))
	for i, c := range counted {
		res[i] = gen.TrendBucket{
			GuestDevices:     c.GuestDevices,
			VouchersRedeemed: c.VouchersRedeemed,
			Bytes:            c.Bytes,
			BytesLowerBound:  c.Wrapped,
			BytesFreshness:   onlineFreshness(store.SessionCounts{OldestOnlineUpdate: c.OldestOpenUpdate}, now),
		}
	}
	return res, nil
}
