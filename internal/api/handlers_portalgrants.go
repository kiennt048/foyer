package api

import (
	"context"
	"errors"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The portal's open authorizations, and ending one on its controller.
// ports_portalgrants.go says why these exist beside
// sessions; disconnectSession (handlers_disconnect.go) is the order this keeps.

func (s *Server) ListPortalGrants(ctx context.Context, request gen.ListPortalGrantsRequestObject) (gen.ListPortalGrantsResponseObject, error) {
	if s.ports.PortalGrants == nil {
		return nil, errNotImplemented
	}
	p := request.Params
	page, err := s.ports.PortalGrants.OpenGrants(ctx, string(p.SiteId), windowFrom(p.Cursor, p.Limit))
	if err != nil {
		return nil, err
	}
	items := page.Items
	if items == nil {
		items = []gen.PortalAuthorization{}
	}
	return gen.ListPortalGrants200JSONResponse{Items: items, NextCursor: page.NextCursor}, nil
}

// DisconnectPortalGrant, in disconnectSession's order:
//
//  1. Refusals that change nothing — the option's own permission, no such grant
//     (404), a grant already over (409 `session-not-live`), a device that is not a
//     controller's to unauthorize (409 `disconnect-unsupported`).
//  2. The withdrawals — the voucher when asked, then the grant — **before** the
//     controller is asked, so the guest cannot re-authorize in between.
//  3. The controller, whose answer is reported, not assumed.
//  4. The audit entry, whatever 3 answered.
func (s *Server) DisconnectPortalGrant(ctx context.Context, request gen.DisconnectPortalGrantRequestObject) (gen.DisconnectPortalGrantResponseObject, error) {
	if s.ports.PortalGrants == nil {
		return nil, errNotImplemented
	}
	revokeVoucher := request.Body != nil && request.Body.RevokeVoucher != nil && *request.Body.RevokeVoucher
	// The option is the act `revokeVoucher` performs, and the gate in front of
	// this handler read only this operation's permission. See
	// TestBodyOptionPermissionsMatchTheContract.
	if revokeVoucher {
		if who := callerFrom(ctx); who == nil || !who.may(gen.VouchersManage) {
			p := forbidden()
			p.Params = map[string]any{"required": string(gen.VouchersManage)}
			return nil, p
		}
		if s.ports.Vouchers == nil {
			return nil, errNotImplemented
		}
	}

	grant, open, err := s.ports.PortalGrants.Grant(ctx, request.GrantId)
	if err != nil {
		return nil, err
	}
	if !open {
		return nil, sessionNotLive()
	}
	if err := s.ports.PortalGrants.CanEndGrant(ctx, grant.Id); err != nil {
		var refusal *DisconnectRefusal
		switch {
		case errors.As(err, &refusal):
			return nil, disconnectUnsupported(refusal)
		case errors.Is(err, ErrDeviceCannotDisconnect):
			return nil, disconnectUnsupported(nil)
		default:
			return nil, err
		}
	}

	out := gen.DisconnectPortalGrant200JSONResponse{}
	who := callerFrom(ctx)
	params := map[string]any{"grantId": grant.Id}
	audit := func() {
		params["disconnected"] = out.Disconnected
		params["grantsRevoked"] = out.GrantsRevoked
		params["voucherRevoked"] = out.VoucherRevoked
		s.recordIn(ctx, who, grant.SiteId, gen.AuditKindSessionDisconnected, gen.AuditSubject{
			// Pseudonymised by an erasure of the device (migration 00052).
			Kind: gen.AuditSubjectKindPortalGrant, Id: grant.Id, Name: ptr(grant.ClientMac),
		}, params)
	}

	if revokeVoucher && grant.VoucherId != nil {
		v, err := s.ports.Vouchers.Revoke(ctx, *grant.VoucherId)
		switch {
		case errors.Is(err, ErrNotFound):
			// Revoked by somebody else in the meantime: not ours to report.
		case err != nil:
			audit()
			return nil, err
		default:
			out.VoucherRevoked = true
			out.Voucher = &v
			params["voucherId"] = v.Id
		}
	}
	switch err := s.ports.PortalGrants.RevokeGrant(ctx, grant.Id); {
	case errors.Is(err, ErrNotFound):
		// Revoked between the read and now; the controller is still asked.
	case err != nil:
		audit()
		return nil, err
	default:
		out.GrantsRevoked = 1
	}

	if err := s.ports.PortalGrants.EndGrant(ctx, grant.Id); err != nil {
		out.Failure = grantEndFailure(grant, err)
	} else {
		out.Disconnected = true
	}
	audit()
	return out, nil
}

// grantEndFailure is sessionEndFailure for a grant: the cause, and the controller
// by name. There is no RADIUS client to report reachability for.
func grantEndFailure(g gen.PortalAuthorization, err error) *gen.Failure {
	params := map[string]any{"cause": string(SessionEndOther)}
	var se *SessionEndError
	if errors.As(err, &se) {
		params["cause"] = string(se.Cause)
		if se.HasErrorCause {
			params["errorCause"] = se.ErrorCause
		}
	}
	if g.DeviceName != nil {
		params["nasName"] = *g.DeviceName
	}
	return &gen.Failure{Type: gen.ProblemssessionEndFailed, Params: &params}
}
