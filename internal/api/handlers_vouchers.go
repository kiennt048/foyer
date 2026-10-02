package api

import (
	"context"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// Vouchers and the batches operators actually work in.
//
// The load-bearing behaviour here is `revokeVoucher`, and it is load-bearing
// because it is honest about a limit: **revoking a voucher that is currently in
// use does not by itself end the session.** Ending it requires a driver call,
// and whether that call can be made depends on the NAS. So the response reports
// what actually happened rather than what was intended — `sessionEnded` is a
// fact, and `failure` carries `/problems/session-end-failed` when the device
// could not be reached.
//
// A server that answered `200` and left the guest online would be the same class
// of defect as a quota figure with no freshness beside it: a console asserting
// something the network never did.

func (s *Server) ListVoucherBatches(ctx context.Context, request gen.ListVoucherBatchesRequestObject) (gen.ListVoucherBatchesResponseObject, error) {
	if s.ports.Vouchers == nil {
		return nil, errNotImplemented
	}
	p := request.Params
	page, err := s.ports.Vouchers.Batches(ctx, string(p.SiteId), windowFrom(p.Cursor, p.Limit))
	if err != nil {
		return nil, err
	}
	items := page.Items
	if items == nil {
		items = []VoucherBatch{}
	}
	return gen.ListVoucherBatches200JSONResponse{Items: items, NextCursor: page.NextCursor}, nil
}

func (s *Server) GetVoucherBatch(ctx context.Context, request gen.GetVoucherBatchRequestObject) (gen.GetVoucherBatchResponseObject, error) {
	if s.ports.Vouchers == nil {
		return nil, errNotImplemented
	}
	b, err := s.ports.Vouchers.Batch(ctx, request.BatchId)
	if err != nil {
		return nil, err
	}
	return gen.GetVoucherBatch200JSONResponse(b), nil
}

func (s *Server) CreateVoucherBatch(ctx context.Context, request gen.CreateVoucherBatchRequestObject) (gen.CreateVoucherBatchResponseObject, error) {
	if s.ports.Vouchers == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body

	errs := map[string]string{}
	faults := map[string]gen.FieldFault{}
	fieldParams := map[string]map[string]any{}
	if strings.TrimSpace(in.Label) == "" {
		errs["label"] = "Required."
		faults["label"] = gen.FieldFaultRequired
	}
	if strings.TrimSpace(in.SiteId) == "" {
		errs["siteId"] = "Required."
		faults["siteId"] = gen.FieldFaultRequired
	}
	if in.Count < 1 {
		errs["count"] = "At least one."
		faults["count"] = gen.FieldFaultBelowMinimum
		fieldParams["count"] = map[string]any{"minimumCount": 1}
	}
	// `from-creation` without a window is a batch that never expires, which is a
	// different product decision from the one this field expresses.
	if in.ExpiryMode == gen.FromCreation && (in.ValidForHours == nil || *in.ValidForHours < 1) {
		errs["validForHours"] = "Required for a batch that expires from creation."
		faults["validForHours"] = gen.FieldFaultRequired
	}
	if len(faults) > 0 {
		return nil, withFieldParams(validation(errs, faults), fieldParams)
	}

	who := callerFrom(ctx)
	actor := ""
	if who != nil {
		actor = who.Operator.Username
	}
	b, err := s.ports.Vouchers.CreateBatch(ctx, in, actor)
	if err != nil {
		return nil, err
	}
	s.recordIn(ctx, who, b.SiteId, gen.AuditKindVoucherBatchCreated, gen.AuditSubject{
		Kind: gen.AuditSubjectKindVoucherBatch, Id: b.Id, Name: ptr(b.Label),
	}, map[string]any{"count": b.Count})
	return gen.CreateVoucherBatch201JSONResponse(b), nil
}

func (s *Server) ListVouchers(ctx context.Context, request gen.ListVouchersRequestObject) (gen.ListVouchersResponseObject, error) {
	if s.ports.Vouchers == nil {
		return nil, errNotImplemented
	}
	p := request.Params
	q := VoucherQuery{
		SiteID:  string(p.SiteId),
		BatchID: text(p.BatchId),
		Search:  text((*string)(p.Q)),
		Window:  windowFrom(p.Cursor, p.Limit),
	}
	if p.State != nil {
		q.States = *p.State
	}
	page, err := s.ports.Vouchers.Vouchers(ctx, q)
	if err != nil {
		return nil, err
	}
	items := page.Items
	if items == nil {
		items = []Voucher{}
	}
	return gen.ListVouchers200JSONResponse{Items: items, NextCursor: page.NextCursor}, nil
}

func (s *Server) RevokeVoucher(ctx context.Context, request gen.RevokeVoucherRequestObject) (gen.RevokeVoucherResponseObject, error) {
	if s.ports.Vouchers == nil {
		return nil, errNotImplemented
	}
	v, err := s.ports.Vouchers.Revoke(ctx, request.VoucherId)
	if err != nil {
		return nil, err
	}

	out := gen.RevokeVoucher200JSONResponse{Voucher: v, SessionEnded: false}

	// A voucher with no session in flight is simply revoked. One with a session
	// needs a driver call, and the response says which of the two happened.
	//
	// The session is looked up by voucher rather than read off
	// `Voucher.currentSessionId`. That field is nullable and can be stale, and a
	// revoke keyed on it would answer `sessionEnded: false` for a guest who is
	// demonstrably still online — the exact shape of dishonesty this operation's
	// `failure` member exists to prevent.
	if live := s.liveSessionFor(ctx, v); live != "" {
		switch {
		case s.ports.Devices == nil:
			out.Failure = &gen.Failure{Type: gen.ProblemssessionEndFailed}
		default:
			if err := s.ports.Devices.EndSession(ctx, live); err != nil {
				f := gen.Failure{Type: gen.ProblemssessionEndFailed}
				if nas := failureContext(ctx, s, live); len(nas) > 0 {
					f.Params = &nas
				}
				out.Failure = &f
			} else {
				out.SessionEnded = true
				// Re-read: ending the session clears the voucher's pointer at
				// it, and answering with the pre-call copy would hand the
				// console a `currentSessionId` for a session that has ended.
				if fresh, err := s.ports.Vouchers.Voucher(ctx, v.Id); err == nil {
					out.Voucher = fresh
				}
			}
		}
	}

	// **A voucher carries no venue of its own** — it belongs to a batch and the
	// batch belongs to a site, which is the same indirection `web/mock` performs
	// and the reason `Voucher` has no `siteId` member to read. One extra read on a
	// revoke, on the operation where a venue matters most: this is the row somebody
	// opens when a guest says their code stopped working.
	site := ""
	if b, err := s.ports.Vouchers.Batch(ctx, v.BatchId); err == nil {
		site = b.SiteId
	}
	s.recordIn(ctx, callerFrom(ctx), site, gen.AuditKindVoucherRevoked, gen.AuditSubject{
		Kind: gen.AuditSubjectKindVoucher, Id: v.Id, Name: ptr(v.Code),
	}, map[string]any{"sessionEnded": out.SessionEnded})
	return out, nil
}

// liveSessionFor finds the session this voucher is being used for, if any.
//
// `online` and `orphaned` both count. An orphaned session is one whose NAS never
// sent an `Acct-Stop` — the classic RADIUS accounting failure — and the guest on
// it may well still be on the network, so treating it as already finished is the
// assumption that leaves a revoked voucher's holder online.
func (s *Server) liveSessionFor(ctx context.Context, v Voucher) string {
	if s.ports.Sessions == nil {
		return ""
	}
	page, err := s.ports.Sessions.Sessions(ctx, SessionQuery{
		VoucherID: v.Id,
		States:    []gen.SessionState{gen.SessionStateOnline, gen.SessionStateOrphaned},
		Window:    Window{Limit: 1},
	})
	if err != nil || len(page.Items) == 0 {
		return ""
	}
	return page.Items[0].Id
}

// failureContext names the device that could not be reached.
//
// The client renders its own copy from `Failure.type`; these are the facts it
// interpolates. Never a sentence — the same rule `Alarm.params` and
// `Problem.params` carry, for the same reason.
func failureContext(ctx context.Context, s *Server, sessionID string) map[string]any {
	if s.ports.Sessions == nil || s.ports.AccessPoints == nil {
		return nil
	}
	sess, err := s.ports.Sessions.SessionByID(ctx, sessionID)
	if err != nil {
		return nil
	}
	n, err := s.ports.AccessPoints.AccessPoint(ctx, sess.NasId)
	if err != nil {
		return nil
	}
	return map[string]any{"nasName": n.Name, "nasReachability": string(n.Reachability)}
}

// ── sessions ─────────────────────────────────────────────────────────────────

// Sessions are read-only, forever.
//
// They are backed by `radacct`, which FreeRADIUS owns. There is no write
// operation in the contract and there never will be — an "edit session"
// affordance can never be built, so this file has two handlers and no third.
func (s *Server) ListSessions(ctx context.Context, request gen.ListSessionsRequestObject) (gen.ListSessionsResponseObject, error) {
	if s.ports.Sessions == nil {
		return nil, errNotImplemented
	}
	p := request.Params
	q := SessionQuery{
		SiteID: string(p.SiteId),
		NasID:  text(p.NasId),
		MAC:    text(p.Mac),
		Search: text((*string)(p.Q)),
		Window: windowFrom(p.Cursor, p.Limit),
	}
	if p.State != nil {
		q.States = *p.State
	}
	page, err := s.ports.Sessions.Sessions(ctx, q)
	if err != nil {
		return nil, err
	}
	items := page.Items
	if items == nil {
		items = []Session{}
	}
	return gen.ListSessions200JSONResponse{Items: items, NextCursor: page.NextCursor}, nil
}

func (s *Server) GetSession(ctx context.Context, request gen.GetSessionRequestObject) (gen.GetSessionResponseObject, error) {
	if s.ports.Sessions == nil {
		return nil, errNotImplemented
	}
	sess, err := s.ports.Sessions.SessionByID(ctx, request.SessionId)
	if err != nil {
		return nil, err
	}
	return gen.GetSession200JSONResponse(sess), nil
}
