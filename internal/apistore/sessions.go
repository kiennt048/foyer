package apistore

import (
	"context"
	"slices"
	"strings"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// SessionLog — read-only, forever.
//
// `radacct` belongs to FreeRADIUS, the application connects as a role holding
// `SELECT` and nothing else on it, and there is no write method here or in the
// repository. An "edit session" affordance can never be built.

// Sessions lists accounting rows.
//
// Three filters the repository cannot take, each named because each is a gap in
// the repository rather than a preference:
//
//   - **`state` is single-valued** in `SessionFilter` and comma-separated in the
//     contract. One state is pushed down; several are filtered here.
//   - **There is no `nasId` filter.** The contract's `listSessions.nasId` is what
//     the console uses to answer "who is on this access point", and it is applied
//     here over rows the repository already resolved. The id it compares is the
//     repository's own: `Session.nasId` is the device id `listNas` hands out.
//   - **There is no voucher filter, and `Session.voucherId` is never populated** —
//     see liveSessionsForVoucher, which is the reason `revokeVoucher` can report
//     `sessionEnded` at all.
//
// `Session.plan` is filled after the page is assembled rather than per row: see
// attachPlans.
func (a *Adapter) Sessions(ctx context.Context, q api.SessionQuery) (api.Page[gen.Session], error) {
	pushed := ""
	if len(q.States) == 1 {
		pushed = string(q.States[0])
	}

	// The voucher filter is resolved before the scan, because it turns into a
	// username to match rather than a column to compare.
	var voucherCode string
	var voucherSession string
	if q.VoucherID != "" {
		code, sessionID, err := a.voucherIdentity(ctx, q.VoucherID)
		if err != nil {
			return api.Page[gen.Session]{}, err
		}
		voucherCode, voucherSession = code, sessionID
	}

	page, err := fill(q.Window,
		func(cursor string, limit int) (store.Page[store.Session], error) {
			return a.st.Accounting().ListSessions(ctx, store.SessionFilter{
				ListOptions: listOptions(q.SiteID, q.Search, cursor, limit),
				State:       pushed,
				MAC:         q.MAC,
			})
		},
		func(s store.Session) (gen.Session, bool) {
			if len(q.States) > 1 && !slices.Contains(q.States, gen.SessionState(s.State)) {
				return gen.Session{}, false
			}
			if q.NasID != "" && s.NasID != q.NasID {
				return gen.Session{}, false
			}
			if q.VoucherID != "" && !sessionUsesVoucher(s, voucherCode, voucherSession) {
				return gen.Session{}, false
			}
			g := sessionOut(s)
			if q.VoucherID != "" {
				// The link is real for this query and is stated on the row it
				// was established for. It is not written back into `radacct` —
				// FreeRADIUS owns that table.
				g.VoucherId = ptr(q.VoucherID)
			}
			return g, true
		},
	)
	if err != nil {
		return api.Page[gen.Session]{}, err
	}
	if err := a.attachPlans(ctx, page.Items); err != nil {
		return api.Page[gen.Session]{}, err
	}
	return page, nil
}

// attachPlans fills `Session.plan` for the rows whose identity Foyer owns.
//
// **After the page rather than inside `keep`**, because it is one statement for the
// whole page instead of two round trips per session, on the screen an operator
// refreshes while a guest stands in front of them. The repository's own comment
// says what the join is and, more importantly, what it refuses to guess.
//
// A row whose identity resolves to nothing keeps a nil plan. That is the answer for
// MAC-auth, for 802.1X, and for a username belonging to no voucher and no account —
// and it must stay nil rather than becoming an empty `Plan`, which would render
// `enforcedBy: unenforced`: a statement about a limit where there is no limit.
func (a *Adapter) attachPlans(ctx context.Context, sessions []gen.Session) error {
	refs := make([]store.IdentityRef, 0, len(sessions))
	seen := make(map[store.IdentityRef]bool, len(sessions))
	for _, s := range sessions {
		if s.Username == nil || *s.Username == "" {
			continue
		}
		ref := store.IdentityRef{SiteID: s.SiteId, Username: *s.Username}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return nil
	}
	plans, err := a.st.SessionPlans().PlansFor(ctx, refs)
	if err != nil {
		return out(err)
	}
	for i := range sessions {
		if sessions[i].Username == nil {
			continue
		}
		p, ok := plans[store.IdentityRef{SiteID: sessions[i].SiteId, Username: *sessions[i].Username}]
		if ok {
			sessions[i].Plan = ptr(planOut(p))
		}
	}
	return nil
}

// voucherIdentity resolves the two things that can tie a session to a voucher.
//
// **`radacct` has no voucher column and never will** — the stock FreeRADIUS column
// set is kept verbatim because it is the project's exit hatch — so the join has to
// come from somewhere else, and there are exactly two candidates:
//
//   - `vouchers.current_session_id`, which is what the voucher row *claims*. It is
//     nullable and it can be stale, so `revokeVoucher`'s own comment is right that a
//     revoke keyed on it alone would answer `sessionEnded: false` for a guest who is
//     demonstrably still online.
//   - **`radacct.username`, which for a redeemed voucher is the code itself.** That
//     is what the NAS puts in User-Name, and what `internal/store` resolves a
//     session's identity kind against (`recordedIdentityKind`).
//
// Both are used, because they fail in different directions: the pointer is stale
// when a session ended without the voucher row being updated, and the username
// match misses a deployment that authenticates a voucher under some other name.
func (a *Adapter) voucherIdentity(ctx context.Context, voucherID string) (code, sessionID string, err error) {
	v, err := a.st.Vouchers().Get(ctx, voucherID)
	if err != nil {
		return "", "", out(err)
	}
	if v.CurrentSessionID != nil {
		sessionID = *v.CurrentSessionID
	}
	return v.Code, sessionID, nil
}

func sessionUsesVoucher(s store.Session, code, sessionID string) bool {
	if sessionID != "" && s.ID == sessionID {
		return true
	}
	return code != "" && s.Username != nil && strings.EqualFold(*s.Username, code)
}

func (a *Adapter) SessionByID(ctx context.Context, id string) (gen.Session, error) {
	s, err := a.st.Accounting().GetSession(ctx, id)
	if err != nil {
		return gen.Session{}, out(err)
	}
	// Through the same helper as the page, and on a slice of one, so that the list
	// and the detail panel cannot answer differently about what a guest was sold.
	one := []gen.Session{sessionOut(*s)}
	if err := a.attachPlans(ctx, one); err != nil {
		return gen.Session{}, err
	}
	return one[0], nil
}
