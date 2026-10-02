package apistore

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/store"
)

// Ending a guest's session, and withdrawing what let them on.
//
// `EndSession` serves both `revokeVoucher` and `disconnectSession`. The second
// needs two things the first does not: an answer **before** any packet — can this
// device end a session at all — and a failure typed finely enough to tell a NAK
// from silence from a wrong shared secret. Both are here, and
// both go through the one resolver below so the question and the act cannot be
// answered by two different drivers.

var _ api.GuestAccess = (*Adapter)(nil)

// sessionTarget is the driver a session's device is behind and the request that
// driver takes to end it.
type sessionTarget struct {
	driver nas.NASDriver
	req    nas.DeauthorizeRequest
	device store.Nas
}

// resolveSessionTarget builds the driver for the device a session is on.
//
// **The controller wins where there is one**, for the reason `deviceDriver`
// gives: a UniFi access point is driven through the controller that adopted it,
// and a per-NAS driver built from its `radius_clients` row would be a CoA client
// aimed at a radio with no CoA listener.
//
// The MAC comes from the accounting row rather than from the caller, because
// `Calling-Station-Id` is what every driver here identifies a client by and
// `radacct` is the only thing that knows which one this session belongs to.
//
// A driver that cannot be built answers `*api.DisconnectRefusal` — the refusal
// `disconnectSession` gives before anything is changed. Any other error is a
// store that could not answer and passes through as one.
func (a *Adapter) resolveSessionTarget(ctx context.Context, sessionID string) (sessionTarget, error) {
	sess, err := a.st.Accounting().GetSession(ctx, sessionID)
	if err != nil {
		return sessionTarget{}, out(err)
	}
	// Used raw: `Session.nasId` is the device id `GetNas` keys on, rather than the
	// `clients.conf` row's.
	device, err := a.st.RadiusClients().GetNas(ctx, sess.NasID)
	if err != nil {
		return sessionTarget{}, out(err)
	}
	refusal := func() error {
		return &api.DisconnectRefusal{Driver: gen.NasDriverKind(device.Driver), NasName: device.Name}
	}
	cannotBuild := func(err error) bool {
		return errors.Is(err, ErrNoDriver) || errors.Is(err, ErrNoNASDriver) || errors.Is(err, ErrCoAVendorUnknown)
	}

	if device.ControllerID == nil {
		// ── the CoA path ─────────────────────────────────────────────────────
		//
		// `DeauthorizeGuest` on this family resolves the session through Foyer's
		// own accounting, checks it belongs to *this* NAS, and returns a typed
		// error carrying `Error-Cause` on a NAK. A device that still cannot name
		// its CoA vendor is refused rather than guessed (`ErrCoAVendorUnknown`).
		d, _, err := a.NASDriverFor(ctx, device.ID)
		if err != nil {
			if cannotBuild(err) {
				return sessionTarget{}, refusal()
			}
			return sessionTarget{}, err
		}
		return sessionTarget{driver: d, device: *device, req: nas.DeauthorizeRequest{
			ClientMAC: sess.CallingStationID,
			// No AP MAC and no vendor site: this device *is* the NAS, and
			// `internal/nas` refuses to synthesise either from the client row.
			Reason: "revoked",
		}}, nil
	}

	vendorSite := ""
	if c, err := a.st.Controllers().Get(ctx, *device.ControllerID); err == nil {
		vendorSite = vendorSiteFor(*c, device.SiteID)
	}
	d, _, err := a.driverFor(ctx, *device.ControllerID, vendorSite)
	if err != nil {
		if cannotBuild(err) {
			return sessionTarget{}, refusal()
		}
		return sessionTarget{}, err
	}
	apMAC := ""
	if device.MAC != nil {
		apMAC = *device.MAC
	}
	return sessionTarget{driver: d, device: *device, req: nas.DeauthorizeRequest{
		ClientMAC: sess.CallingStationID,
		APMAC:     apMAC,
		SiteID:    vendorSite,
		Reason:    "revoked",
	}}, nil
}

// CanEndSession answers from the **driver**, never from the stored capability.
//
// The column is a claim, written by whatever last probed the device;
// the driver is the fact. A row claiming `canDeauthGuest: true` on a Ruckus
// device must not send the operator through a revocation and then into a
// refusal. Nothing is dialled here — building a driver opens no socket.
func (a *Adapter) CanEndSession(ctx context.Context, sessionID string) error {
	t, err := a.resolveSessionTarget(ctx, sessionID)
	if err != nil {
		return err
	}
	if !t.driver.Capabilities().CanDeauthGuest {
		return &api.DisconnectRefusal{Driver: gen.NasDriverKind(t.device.Driver), NasName: t.device.Name}
	}
	return nil
}

// EndSession disconnects a guest.
//
// This is what `revokeVoucher` and `disconnectSession` both need, and whether it
// can be done at all depends on the NAS — so the failure is reported rather than
// assumed, typed as `*api.SessionEndError` so the console can say *which* failure.
func (a *Adapter) EndSession(ctx context.Context, sessionID string) error {
	t, err := a.resolveSessionTarget(ctx, sessionID)
	if err != nil {
		return err
	}
	return sessionEndError(t.device, t.driver.DeauthorizeGuest(ctx, t.req))
}

// sessionEndError classifies a driver's failure into the contract's four causes.
//
// The order is the specificity: a UniFi driver that fell back to CoA wraps the
// CoA error inside its own, and the CoA error is the more useful of the two.
func sessionEndError(device store.Nas, err error) error {
	if err == nil {
		return nil
	}
	var coaErr *nas.CoAError
	var unsupported *nas.UnsupportedActionError
	var apiErr *nas.APIError
	var netErr net.Error
	switch {
	case errors.As(err, &unsupported):
		return &api.DisconnectRefusal{Driver: gen.NasDriverKind(device.Driver), NasName: device.Name}
	case errors.As(err, &coaErr):
		return &api.SessionEndError{Cause: api.SessionEndRefused, ErrorCause: coaErr.ErrorCause,
			HasErrorCause: coaErr.HasErrorCause, Err: err}
	case errors.Is(err, nas.ErrCoABadAuthenticator):
		return &api.SessionEndError{Cause: api.SessionEndBadSecret, Err: err}
	case errors.Is(err, nas.ErrCoATimeout):
		return &api.SessionEndError{Cause: api.SessionEndNoAnswer, Err: err}
	case errors.As(err, &apiErr):
		// The controller answered and said no. Its body is never forwarded.
		return &api.SessionEndError{Cause: api.SessionEndRefused, Err: err}
	case errors.As(err, &netErr) && netErr.Timeout():
		return &api.SessionEndError{Cause: api.SessionEndNoAnswer, Err: err}
	default:
		return &api.SessionEndError{Cause: api.SessionEndOther, Err: err}
	}
}

// RevokeGrantsForSession revokes every open portal grant for this session's
// client MAC at its venue.
//
// By MAC and venue rather than by one grant id, because `radacct` records no
// grant and a device may hold several — one per voucher it redeemed. A grant
// whose time has run out is left alone: it is already over, and restamping it
// `revoked` would rewrite what the portal tells a guest about their last visit.
func (a *Adapter) RevokeGrantsForSession(ctx context.Context, sessionID string) (int, error) {
	sess, err := a.st.Accounting().GetSession(ctx, sessionID)
	if err != nil {
		return 0, out(err)
	}
	n, err := a.st.Grants().RevokeOpenGrantsForMAC(ctx, sess.SiteID, sess.CallingStationID, a.now())
	if err != nil {
		return 0, out(err)
	}
	return int(n), nil
}

// VoucherForSession names the voucher a session was admitted with, or "".
//
// Three candidates, the same two `voucherIdentity` trusts plus the portal's own
// record, because they fail in different directions:
//
//  1. a voucher whose `current_session_id` is this session — the voucher row's
//     claim;
//  2. a voucher whose code is the RADIUS `User-Name` at this venue — which is
//     what a NAS sends for a code redeemed over RADIUS;
//  3. the newest open portal grant for this device at this venue that names a
//     voucher — the controller-API path, where `User-Name` is not the code.
//
// A voucher already revoked is not named: there is nothing left to revoke, and
// answering it would make the second press report a revocation that did not
// happen.
func (a *Adapter) VoucherForSession(ctx context.Context, sessionID string) (string, error) {
	sess, err := a.st.Accounting().GetSession(ctx, sessionID)
	if err != nil {
		return "", out(err)
	}
	if id, err := a.st.Vouchers().VoucherForSession(ctx, sess.SiteID, sess.ID); err == nil {
		return id, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", out(err)
	}
	if sess.Username != nil && strings.TrimSpace(*sess.Username) != "" {
		v, err := a.st.Vouchers().GetByCode(ctx, strings.TrimSpace(*sess.Username))
		switch {
		case err == nil:
			if v.SiteID == sess.SiteID && v.State != string(gen.VoucherStateRevoked) {
				return v.ID, nil
			}
		case !errors.Is(err, store.ErrNotFound):
			return "", out(err)
		}
	}
	id, err := a.st.Grants().OpenGrantVoucherForMAC(ctx, sess.SiteID, sess.CallingStationID, a.now())
	switch {
	case err == nil:
		return id, nil
	case errors.Is(err, store.ErrNotFound):
		return "", nil
	default:
		return "", out(err)
	}
}
