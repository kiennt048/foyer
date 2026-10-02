package apistore

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// VoucherStore — batches and the codes in them.

func (a *Adapter) Batches(ctx context.Context, siteID string, w api.Window) (api.Page[gen.VoucherBatch], error) {
	return fill(w,
		func(cursor string, limit int) (store.Page[store.VoucherBatch], error) {
			return a.st.Vouchers().ListBatches(ctx, listOptions(siteID, "", cursor, limit))
		},
		func(b store.VoucherBatch) (gen.VoucherBatch, bool) { return batchOut(b), true },
	)
}

func (a *Adapter) Batch(ctx context.Context, id string) (gen.VoucherBatch, error) {
	b, err := a.st.Vouchers().GetBatch(ctx, id)
	if err != nil {
		return gen.VoucherBatch{}, out(err)
	}
	return batchOut(*b), nil
}

// CreateBatch generates the codes and writes the batch in one transaction.
//
// One transaction because a half-created batch is a printed sheet of codes that do
// not all work, and the operator has no way to tell which. The codes come from
// `internal/identity`, which draws from `crypto/rand` over an alphabet with no O,
// no I, no L, no 0 and no 1 — a voucher is read off a slip by somebody at a counter
// and typed into a phone, often in a second language, and those are the characters
// two people disagree about.
func (a *Adapter) CreateBatch(ctx context.Context, in gen.VoucherBatchCreate, actorUsername string) (gen.VoucherBatch, error) {
	enforced, err := a.enforcementIn(ctx, in.SiteId, in.Plan)
	if err != nil {
		return gen.VoucherBatch{}, err
	}

	b := &store.VoucherBatch{
		ID:            a.newID("batch-"),
		SiteID:        in.SiteId,
		Label:         in.Label,
		Count:         in.Count,
		ExpiryMode:    string(in.ExpiryMode),
		Plan:          planIn(in.Plan, enforced),
		ValidForHours: in.ValidForHours,
	}
	if actorUsername != "" {
		b.CreatedBy = ptr(actorUsername)
	}
	// `from-creation` stamps every code now; `from-first-use` leaves `expires_at`
	// NULL until redemption, which is why a batch printed in January is still worth
	// selling in March.
	if in.ExpiryMode == gen.FromCreation && in.ValidForHours != nil {
		b.ExpiresAt = ptr(a.now().Add(time.Duration(*in.ValidForHours) * time.Hour))
	}

	codes, err := identity.GenerateBatchCodes(in.Count, voucherCodeLength)
	if err != nil {
		return gen.VoucherBatch{}, err
	}
	vouchers := make([]store.NewVoucher, 0, len(codes))
	for i, code := range codes {
		vouchers = append(vouchers, store.NewVoucher{ID: b.ID + "-" + strconv.Itoa(i), Code: code})
	}
	if err := a.st.Vouchers().CreateBatch(ctx, b, vouchers); err != nil {
		return gen.VoucherBatch{}, out(err)
	}

	// Read back rather than compose: the histogram is aggregated from the voucher
	// rows, and a batch that reported its counts from the request would be
	// reporting what was asked for rather than what landed.
	fresh, err := a.st.Vouchers().GetBatch(ctx, b.ID)
	if err != nil {
		return gen.VoucherBatch{}, out(err)
	}
	return batchOut(*fresh), nil
}

// voucherCodeLength is twelve characters from a 31-symbol alphabet.
//
// A session's identity kind is read from the vouchers table first
// (`store.recordedIdentityKind`); this shape is only the fallback for a code no
// table holds any more, so shortening it changes how those few are classed.
const voucherCodeLength = 12

// enforcementIn answers *who* will enforce this plan, from the **weakest** enabled
// device in the site rather than the strongest.
//
// A guest who lands on the one access point that cannot apply a rate has an
// unenforced rate, whatever the other eleven do, and a voucher batch is redeemable
// anywhere in the site so it cannot know which one that will be. Same rule as the
// walled garden's effective kind.
//
// Three limits, three different answers, and the differences are protocol facts
// rather than preferences:
//
//   - **Duration** is `Session-Timeout` in the Access-Accept. Standard RADIUS,
//     honoured by every driver here, gated on no capability — so a duration-only
//     plan is `nas`.
//   - **A data allowance** needs `Capabilities.HardQuota` to be a cap. Without it,
//     it is `foyer-sampled`: `radacct` byte counters advance only on Interim-Update
//     or Stop and `Acct-Interim-Interval` has a 300-second floor, so it is a
//     backstop with overshoot — which is what that value means, and is not the same
//     as nothing enforcing it.
//   - **A rate limit** needs `Capabilities.BandwidthLimits`. There is no sampled
//     equivalent: nothing can retroactively slow a link, so without the capability
//     the figure is `unenforced` and the UI must not present it as a cap.
func (a *Adapter) enforcementIn(ctx context.Context, siteID string, p gen.PlanRequest) (gen.PlanEnforcedBy, error) {
	wantsRate := p.DownKbps != nil || p.UpKbps != nil
	wantsQuota := p.DataAllowanceMb != nil
	wantsDuration := p.DurationMinutes != nil
	if !wantsRate && !wantsQuota && !wantsDuration {
		return gen.PlanEnforcedByUnenforced, nil
	}

	// `ListClients` already returns only the enabled rows, which is the population
	// this asks about: a disabled client enforces nothing because nothing reaches
	// it.
	clients, err := a.st.RadiusClients().ListClients(ctx, siteID)
	if err != nil {
		return "", out(err)
	}
	if len(clients) == 0 {
		// A site with no enabled device enforces nothing, and saying `nas` would
		// name a device that is not there.
		return gen.PlanEnforcedByUnenforced, nil
	}

	everyDeviceRates, everyDeviceQuotas := true, true
	for _, c := range clients {
		if !c.Capabilities.BandwidthLimits {
			everyDeviceRates = false
		}
		if !c.Capabilities.HardQuota {
			everyDeviceQuotas = false
		}
	}
	switch {
	case wantsRate && !everyDeviceRates:
		return gen.PlanEnforcedByUnenforced, nil
	case wantsQuota && !everyDeviceQuotas:
		return gen.PlanEnforcedByFoyerSampled, nil
	default:
		return gen.PlanEnforcedByNas, nil
	}
}

// Vouchers lists codes.
//
// `state` is the one filter the repository cannot take whole: `VoucherFilter.State`
// is a single value and the contract's parameter is a comma-separated list, so one
// state is pushed down and several are filtered here. **Reported** — the repository
// wants `States []string`, and doing it here costs a repository read per rejected
// row.
//
// The search is normalised the way a guest types a code: upper-cased with group
// separators stripped, so a code copied with or without dashes finds the same row.
func (a *Adapter) Vouchers(ctx context.Context, q api.VoucherQuery) (api.Page[gen.Voucher], error) {
	pushed := ""
	if len(q.States) == 1 {
		pushed = string(q.States[0])
	}
	needle := normaliseCode(q.Search)

	return fill(q.Window,
		func(cursor string, limit int) (store.Page[store.Voucher], error) {
			return a.st.Vouchers().ListVouchers(ctx, store.VoucherFilter{
				ListOptions: listOptions(q.SiteID, needle, cursor, limit),
				BatchID:     q.BatchID,
				State:       pushed,
			})
		},
		func(v store.Voucher) (gen.Voucher, bool) {
			if len(q.States) > 1 && !slices.Contains(q.States, gen.VoucherState(v.State)) {
				return gen.Voucher{}, false
			}
			return voucherOut(v), true
		},
	)
}

// normaliseCode drops anything outside the voucher alphabet and upper-cases the
// rest, so a code pasted with dashes and one pasted without are the same code.
func normaliseCode(v string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(v) {
		if strings.ContainsRune(identity.UnambiguousCharacterSet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (a *Adapter) Voucher(ctx context.Context, id string) (gen.Voucher, error) {
	v, err := a.st.Vouchers().Get(ctx, id)
	if err != nil {
		return gen.Voucher{}, out(err)
	}
	return voucherOut(*v), nil
}

// Revoke ends a voucher's life.
//
// The repository refuses to revoke one that is already revoked rather than silently
// succeeding, so that the audit log records one revocation rather than however many
// times an operator pressed the button. That refusal arrives as `ErrNotFound`,
// which is the right answer for the second press: there is no voucher in a
// revocable state under that id.
func (a *Adapter) Revoke(ctx context.Context, id string) (gen.Voucher, error) {
	v, err := a.st.Vouchers().Revoke(ctx, id, a.now())
	if err != nil {
		return gen.Voucher{}, out(err)
	}
	return voucherOut(*v), nil
}
