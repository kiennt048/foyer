package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The `revokeVoucher` option on `disconnectSession` is the act the `revokeVoucher`
// operation performs, and that operation is gated on `vouchers:manage`. The gate
// in front of a handler reads one `x-permission`, so the handler is where the
// second one is checked — and this is the test that makes it fail if it is not.
//
// Internal, because the two roles the contract defines both hold both
// permissions: the refusal can only be driven by a caller whose permission set no
// role produces today, which is the caller this check exists for.

// sessionsThatMustNotBeRead fails the test if the handler reads anything on
// behalf of a caller it should already have refused.
type sessionsThatMustNotBeRead struct {
	SessionLog
	t    *testing.T
	read bool
}

func (s *sessionsThatMustNotBeRead) SessionByID(context.Context, string) (Session, error) {
	s.read = true
	return Session{}, ErrNotFound
}

type devicesThatMustNotBeAsked struct{ DeviceOperations }

func TestDisconnectRevokeVoucherNeedsVouchersManage(t *testing.T) {
	yes := true
	call := func(perms []gen.OperatorPermission, revokeVoucher bool) (error, bool) {
		sessions := &sessionsThatMustNotBeRead{t: t}
		s := &Server{ports: Ports{Sessions: sessions, Devices: devicesThatMustNotBeAsked{}}}
		ctx := withCaller(context.Background(), &caller{Permissions: perms})
		body := &gen.DisconnectSessionJSONRequestBody{}
		if revokeVoucher {
			body.RevokeVoucher = &yes
		}
		_, err := s.DisconnectSession(ctx, gen.DisconnectSessionRequestObject{SessionId: "sess-1", Body: body})
		return err, sessions.read
	}

	counter := []gen.OperatorPermission{gen.SessionsRead, gen.SessionsManage}

	t.Run("sessions:manage alone, asking for the voucher", func(t *testing.T) {
		err, read := call(counter, true)
		var p *apiError
		if !errors.As(err, &p) || p.Status != http.StatusForbidden || p.Type != gen.Problemsforbidden {
			t.Fatalf("answered %v, want 403 /problems/forbidden", err)
		}
		if p.Params["required"] != string(gen.VouchersManage) {
			t.Errorf("params.required = %v, want %q", p.Params["required"], gen.VouchersManage)
		}
		if read {
			t.Error("the session was read on behalf of a caller the handler refuses")
		}
	})

	t.Run("sessions:manage alone, a plain disconnect", func(t *testing.T) {
		// Not refused on permission: it reaches the session lookup, which this
		// stub answers 404.
		if err, read := call(counter, false); !errors.Is(err, ErrNotFound) || !read {
			t.Fatalf("a plain disconnect answered %v (read=%v), want the session lookup's 404", err, read)
		}
	})

	t.Run("with vouchers:manage", func(t *testing.T) {
		perms := append(counter, gen.VouchersManage)
		if err, read := call(perms, true); !errors.Is(err, ErrNotFound) || !read {
			t.Fatalf("a caller holding both answered %v (read=%v), want the session lookup's 404", err, read)
		}
	})

	t.Run("both shipped roles hold both", func(t *testing.T) {
		// The check must not take the option away from anyone who has it today.
		for _, role := range []gen.OperatorRole{gen.Administrator, gen.Staff} {
			c := caller{Permissions: permissionsFor(role)}
			if !c.may(gen.SessionsManage) || !c.may(gen.VouchersManage) {
				t.Errorf("%s does not hold both sessions:manage and vouchers:manage", role)
			}
		}
	})
}
