package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// `disconnectSession` — the one action on a session, and not an edit of one.
//
// ── what the field does, and where this follows it ───────────────────────────
//
// Surveyed before this was designed:
//
//   - **ClearPass Guest** (Guest › Active Sessions) disconnects or reauthorizes a
//     live session over RFC 3576/5176 and gates it on an operator-profile
//     privilege. The visitor account is left alone: ending the session and ending
//     the right to one are two acts.
//   - **PacketFence** draws the same line in other words — *deauthenticate* ends
//     the session through the switch/controller module, *unregister* withdraws the
//     node's access — and its documentation is the reason Ruckus is not a CoA
//     vendor here: RADIUS Disconnect "fails" on SmartZone in non-proxy mode, and
//     the Northbound API has to be used instead (`docs/rules/nas-drivers.md`).
//   - **UniFi**'s own hotspot manager, and every third-party one built on its
//     private API, revokes a guest with `cmd/stamgr` `unauthorize-guest`; the
//     Integration API's equivalent is `UNAUTHORIZE_GUEST_ACCESS`.
//
// So: one operation, the two withdrawals as **options the operator chooses**,
// the device reached through the driver's own capability, and a refusal — not a
// silent no-op — where the driver has none. Where this diverges from ClearPass is
// deliberate and small: ClearPass has no way to revoke the voucher in the same
// act, and a front-desk operator throwing a guest off almost always wants the
// code dead too; making it a second trip to another screen is how it gets
// forgotten.
//
// ── the order, which is the part that matters ────────────────────────────────
//
//  1. Refusals that change nothing: no such session (404), already ended (409
//     `session-not-live`), a driver that cannot end one (409
//     `disconnect-unsupported`). All three are answered before anything is
//     written, so an operator told "no" finds nothing else changed.
//  2. The withdrawals the operator asked for — grant, then voucher. **Before**
//     the device is asked, because the other order leaves a window in which a
//     RADIUS client re-authenticates with the voucher it still holds.
//  3. The device. Its answer is reported, not assumed: a NAK, silence and a
//     wrong shared secret are three different afternoons.
//  4. The audit entry, whatever 3 answered — an attempt that revoked a voucher
//     is a change.
func (s *Server) DisconnectSession(ctx context.Context, request gen.DisconnectSessionRequestObject) (gen.DisconnectSessionResponseObject, error) {
	if s.ports.Sessions == nil || s.ports.Devices == nil {
		return nil, errNotImplemented
	}
	// `revokeVoucher` is the act `revokeVoucher` (the operation) performs, and that
	// operation is gated on `vouchers:manage`. The gate in front of this handler
	// read only this operation's `x-permission`, so without this an option would be
	// the way round another operation's permission. Refused first: nothing is read
	// on the caller's behalf that they could not have asked for.
	if b := request.Body; b != nil && b.RevokeVoucher != nil && *b.RevokeVoucher {
		if who := callerFrom(ctx); who == nil || !who.may(gen.VouchersManage) {
			p := forbidden()
			p.Params = map[string]any{"required": string(gen.VouchersManage)}
			return nil, p
		}
	}
	sess, err := s.ports.Sessions.SessionByID(ctx, request.SessionId)
	if err != nil {
		return nil, err
	}
	// `orphaned` and `unknown` are both disconnectable, for the reason
	// `liveSessionFor` gives: an orphaned row is a NAS that never sent Acct-Stop,
	// and the guest on it may well still be online. Only a recorded stop is a
	// session that is over.
	if sess.State == gen.SessionStateEnded {
		return nil, sessionNotLive()
	}
	if err := s.ports.Devices.CanEndSession(ctx, sess.Id); err != nil {
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

	var revokeGrant, revokeVoucher bool
	if b := request.Body; b != nil {
		revokeGrant = b.RevokeGrant != nil && *b.RevokeGrant
		revokeVoucher = b.RevokeVoucher != nil && *b.RevokeVoucher
	}
	if (revokeGrant || revokeVoucher) && s.ports.Guests == nil || revokeVoucher && s.ports.Vouchers == nil {
		// Asked for a withdrawal this server cannot make. Refusing the whole call
		// rather than disconnecting without it: a guest thrown off with the
		// voucher still live is the outcome the operator ticked the box to avoid.
		return nil, errNotImplemented
	}

	out := gen.DisconnectSession200JSONResponse{}
	who := callerFrom(ctx)
	params := map[string]any{"nasId": sess.NasId}
	audit := func() {
		params["disconnected"] = out.Disconnected
		params["grantsRevoked"] = out.GrantsRevoked
		params["voucherRevoked"] = out.VoucherRevoked
		s.recordIn(ctx, who, sess.SiteId, gen.AuditKindSessionDisconnected, gen.AuditSubject{
			// The MAC in full, so an operator can match the entry to the guest at the
			// desk; an erasure of the device pseudonymises it (migration 00052).
			Kind: gen.AuditSubjectKindSession, Id: sess.Id, Name: ptr(sess.CallingStationId),
		}, params)
	}

	// ── 2. the withdrawals ───────────────────────────────────────────────────
	if revokeVoucher {
		// Resolved before the grant is revoked: one of the ways a voucher is
		// found is through the grant, and a revoked grant is not an open one.
		id, err := s.ports.Guests.VoucherForSession(ctx, sess.Id)
		if err != nil {
			return nil, err
		}
		if id != "" {
			v, err := s.ports.Vouchers.Revoke(ctx, id)
			switch {
			case errors.Is(err, ErrNotFound):
				// Revoked by somebody else between the lookup and now. Not ours
				// to report as revoked, and not a failure.
			case err != nil:
				audit()
				return nil, err
			default:
				out.VoucherRevoked = true
				out.Voucher = &v
				params["voucherId"] = v.Id
			}
		}
	}
	if revokeGrant {
		n, err := s.ports.Guests.RevokeGrantsForSession(ctx, sess.Id)
		if err != nil {
			audit()
			return nil, err
		}
		out.GrantsRevoked = n
	}

	// ── 3. the device ────────────────────────────────────────────────────────
	if err := s.ports.Devices.EndSession(ctx, sess.Id); err != nil {
		out.Failure = sessionEndFailure(ctx, s, sess, err)
	} else {
		out.Disconnected = true
	}
	// Re-read the voucher: ending the session clears its pointer at it, and the
	// pre-call copy would hand the console a `currentSessionId` that has ended.
	if out.Voucher != nil {
		if fresh, err := s.ports.Vouchers.Voucher(ctx, out.Voucher.Id); err == nil {
			out.Voucher = &fresh
		}
	}

	// ── 4. the record ────────────────────────────────────────────────────────
	audit()
	return out, nil
}

// sessionEndFailure renders a device's refusal as the `Failure` the contract
// declares: `session-end-failed`, with the device and **why**, never a sentence.
func sessionEndFailure(ctx context.Context, s *Server, sess Session, err error) *gen.Failure {
	params := map[string]any{"cause": string(SessionEndOther)}
	var se *SessionEndError
	if errors.As(err, &se) {
		params["cause"] = string(se.Cause)
		if se.HasErrorCause {
			params["errorCause"] = se.ErrorCause
		}
	}
	for k, v := range failureContext(ctx, s, sess.Id) {
		params[k] = v
	}
	return &gen.Failure{Type: gen.ProblemssessionEndFailed, Params: &params}
}

// sessionNotLive: the session has a recorded stop. Nothing to end.
func sessionNotLive() *apiError {
	return &apiError{Status: http.StatusConflict, Type: gen.ProblemssessionNotLive, Title: "This session has already ended"}
}

// disconnectUnsupported: the device's driver cannot end a session. The console
// hides the control on such a device; this is what a client that did not — or a
// stored capability that disagrees with the driver — is told.
func disconnectUnsupported(r *DisconnectRefusal) *apiError {
	e := &apiError{Status: http.StatusConflict, Type: gen.ProblemsdisconnectUnsupported, Title: "This device cannot end a session from Foyer"}
	if r != nil {
		e.Params = map[string]any{"driver": string(r.Driver), "nasName": r.NasName}
	}
	return e
}
