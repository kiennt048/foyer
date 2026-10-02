package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// fakeGrants records what disconnectPortalGrant asked of it, in order.
type fakeGrants struct {
	grant   gen.PortalAuthorization
	open    bool
	refuse  error
	endErr  error
	calls   []string
	lookups int
}

func (f *fakeGrants) OpenGrants(context.Context, string, Window) (Page[gen.PortalAuthorization], error) {
	return Page[gen.PortalAuthorization]{}, nil
}
func (f *fakeGrants) Grant(context.Context, string) (gen.PortalAuthorization, bool, error) {
	f.lookups++
	return f.grant, f.open, nil
}
func (f *fakeGrants) CanEndGrant(context.Context, string) error {
	f.calls = append(f.calls, "can")
	return f.refuse
}
func (f *fakeGrants) RevokeGrant(context.Context, string) error {
	f.calls = append(f.calls, "revoke")
	return nil
}
func (f *fakeGrants) EndGrant(context.Context, string) error {
	f.calls = append(f.calls, "end")
	return f.endErr
}

func grantServer(f *fakeGrants) *Server { return &Server{ports: Ports{PortalGrants: f}} }

func adminCaller() context.Context {
	return withCaller(context.Background(), &caller{Permissions: permissionsFor(gen.Administrator)})
}

func TestDisconnectPortalGrantWithdrawsTheGrantBeforeAskingTheController(t *testing.T) {
	f := &fakeGrants{open: true, grant: gen.PortalAuthorization{Id: "g-1", SiteId: "s", ClientMac: "02:00:00:00:00:01", GrantedAt: time.Now()}}
	resp, err := grantServer(f).DisconnectPortalGrant(adminCaller(), gen.DisconnectPortalGrantRequestObject{GrantId: "g-1", Body: &gen.DisconnectPortalGrantJSONRequestBody{}})
	if err != nil {
		t.Fatalf("DisconnectPortalGrant: %v", err)
	}
	out := resp.(gen.DisconnectPortalGrant200JSONResponse)
	if !out.Disconnected || out.GrantsRevoked != 1 {
		t.Fatalf("answer = %+v", out)
	}
	if got := len(f.calls); got != 3 || f.calls[0] != "can" || f.calls[1] != "revoke" || f.calls[2] != "end" {
		t.Fatalf("order = %v; the grant must be withdrawn before the controller is asked", f.calls)
	}

	// A controller that refused: reported, with the grant already withdrawn.
	f = &fakeGrants{open: true, grant: f.grant, endErr: &SessionEndError{Cause: SessionEndRefused}}
	resp, err = grantServer(f).DisconnectPortalGrant(adminCaller(), gen.DisconnectPortalGrantRequestObject{GrantId: "g-1", Body: &gen.DisconnectPortalGrantJSONRequestBody{}})
	if err != nil {
		t.Fatal(err)
	}
	out = resp.(gen.DisconnectPortalGrant200JSONResponse)
	if out.Disconnected || out.Failure == nil || (*out.Failure.Params)["cause"] != string(SessionEndRefused) {
		t.Fatalf("a refused unauthorize answered %+v", out)
	}
}

func TestDisconnectPortalGrantRefusesBeforeAnythingChanges(t *testing.T) {
	grant := gen.PortalAuthorization{Id: "g-1", SiteId: "s", ClientMac: "02:00:00:00:00:01", GrantedAt: time.Now()}
	for name, tc := range map[string]struct {
		f      *fakeGrants
		status int
		typ    gen.ProblemType
	}{
		"already over":     {&fakeGrants{grant: grant, open: false}, http.StatusConflict, gen.ProblemssessionNotLive},
		"not a controller": {&fakeGrants{grant: grant, open: true, refuse: &DisconnectRefusal{Driver: gen.NasDriverKindMikrotik, NasName: "Lobby"}}, http.StatusConflict, gen.ProblemsdisconnectUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := grantServer(tc.f).DisconnectPortalGrant(adminCaller(), gen.DisconnectPortalGrantRequestObject{GrantId: "g-1", Body: &gen.DisconnectPortalGrantJSONRequestBody{}})
			var p *apiError
			if !errors.As(err, &p) || p.Status != tc.status || p.Type != tc.typ {
				t.Fatalf("answered %v, want %d %s", err, tc.status, tc.typ)
			}
			for _, c := range tc.f.calls {
				if c == "revoke" || c == "end" {
					t.Fatalf("a refusal still did %q", c)
				}
			}
		})
	}
}

// The option's own permission, as disconnectSession's: refused before the grant is
// even read.
func TestDisconnectPortalGrantRevokeVoucherNeedsVouchersManage(t *testing.T) {
	yes := true
	f := &fakeGrants{open: true}
	ctx := withCaller(context.Background(), &caller{Permissions: []gen.OperatorPermission{gen.SessionsRead, gen.SessionsManage}})
	_, err := grantServer(f).DisconnectPortalGrant(ctx, gen.DisconnectPortalGrantRequestObject{
		GrantId: "g-1", Body: &gen.DisconnectPortalGrantJSONRequestBody{RevokeVoucher: &yes}})
	var p *apiError
	if !errors.As(err, &p) || p.Status != http.StatusForbidden || p.Params["required"] != string(gen.VouchersManage) {
		t.Fatalf("answered %v, want 403 requiring vouchers:manage", err)
	}
	if f.lookups != 0 {
		t.Error("the grant was read on behalf of a caller the handler refuses")
	}
}
