package otp

import (
	"context"
	"errors"

	"github.com/kiennt048/foyer/internal/portal"
)

// PortalCodes is this service as `internal/portal`'s `PhoneCodes` port.
//
// The dependency points this way on purpose: `internal/portal` imports nothing but
// `internal/nas`, so it declares the port and its errors and this
// package adapts to them — the same shape `internal/apistore` uses for every other
// portal port. Both the SQL wiring and the in-memory API world use this one
// mapping, so the two cannot answer a guest differently.
func PortalCodes(s *Service) portal.PhoneCodes { return portalCodes{s} }

type portalCodes struct{ svc *Service }

func (p portalCodes) Offered(ctx context.Context, siteID string) (int, bool) {
	return p.svc.Offered(ctx, siteID)
}

func (p portalCodes) Send(ctx context.Context, r portal.PhoneCodeRequest) error {
	return portalError(p.svc.Send(ctx, Request{
		SiteID: r.SiteID, SessionTokenHash: r.SessionTokenHash,
		ClientMAC: r.ClientMAC, ClientIP: r.ClientIP, Phone: r.Phone,
	}))
}

func (p portalCodes) Check(ctx context.Context, sessionTokenHash, code string) (string, error) {
	phone, err := p.svc.Check(ctx, sessionTokenHash, code)
	return phone, portalError(err)
}

func portalError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotOffered):
		return portal.ErrPhoneNotOffered
	case errors.Is(err, ErrPhoneInvalid):
		return portal.ErrPhoneInvalid
	case errors.Is(err, ErrLimited):
		return portal.ErrPhoneLimited
	case errors.Is(err, ErrCodeWrong):
		return portal.ErrCodeWrong
	case errors.Is(err, ErrCodeExpired):
		return portal.ErrCodeExpired
	default:
		// The ceiling, a failed send, or anything unforeseen: the venue cannot
		// send right now, and the guest is sent to another method.
		return portal.ErrPhoneCannotSend
	}
}
