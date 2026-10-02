package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
	"github.com/kiennt048/foyer/internal/portal"
)

// `disconnectSession`, driven over HTTP against the in-memory fake.
//
// **What this proves and what it does not.** The fake stands in for the device,
// so nothing here is evidence about a NAS: the RFC 5176 exchange is proven in
// `internal/nas` against a UDP listener and end to end in `internal/apistore`
// against PostgreSQL and a fake NAS. What this proves is the API layer — who may
// call it, the refusals that change nothing, the order of the withdrawals, what
// the answer says and what the audit log records.

const (
	liveSession  = "sess-live"
	endedSession = "sess-ended"
	omadaSession = "sess-omada"
	dcGuestMAC   = "1c:71:25:63:e4:24"
	voucherLive  = "v-live"
	grantLive    = "grant-live"
)

func disconnectRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	w := r.store.World()
	// nas-1 can end a session; nas-2 (Omada, no driver) cannot.
	w.Nas[0].Capabilities.CanDeauthGuest = true
	w.Nas[1].Capabilities.CanDeauthGuest = false

	session := func(id, nasID string, state gen.SessionState) gen.Session {
		return gen.Session{
			Id: id, SiteId: testSite, NasId: nasID, NasName: ptr("Front of house"),
			State: state, CallingStationId: guestMAC, StartedAt: at(-1),
			Username:  ptr("K7Q2M9P4X3WZ"),
			Freshness: gen.Freshness{State: gen.FreshnessStateSampled, SampledAt: ptr(at(0))},
		}
	}
	w.Sessions = []gen.Session{
		session(liveSession, "nas-1", gen.SessionStateOnline),
		session(endedSession, "nas-1", gen.SessionStateEnded),
		session(omadaSession, "nas-2", gen.SessionStateOnline),
	}
	w.Vouchers = []gen.Voucher{{
		Id: voucherLive, BatchId: "batch-1", Code: "K7Q2-M9P4-X3WZ",
		State: gen.VoucherStateInUse, CurrentSessionId: ptr(liveSession),
	}}

	r.store.PortalStore = portal.NewMemoryStore()
	if err := r.store.PortalStore.CreateGrant(context.Background(), portal.Grant{
		ID: grantLive, SiteID: testSite, ClientMAC: guestMAC,
		Method: portal.MethodVoucher, VoucherID: voucherLive, GrantedAt: at(-1),
	}); err != nil {
		t.Fatalf("seeding a grant: %v", err)
	}
	return r
}

func ptr[T any](v T) *T { return &v }

func (r *rig) disconnect(sessionID string, body map[string]any) (*http.Response, []byte) {
	r.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	resp := r.do(http.MethodPost, "/sessions/"+sessionID+"/disconnect", body)
	defer resp.Body.Close()
	raw := []byte(readAll(r.t, resp))
	return resp, raw
}

func worldAuditOf(s *memory.Store, kind gen.AuditKind) []gen.AuditEntry {
	var out []gen.AuditEntry
	for _, e := range s.World().Audit {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func grantRevoked(t *testing.T, s *memory.Store) bool {
	t.Helper()
	g, err := s.PortalStore.Grant(context.Background(), grantLive)
	if err != nil {
		t.Fatalf("reading the grant back: %v", err)
	}
	return g.RevokedAt != nil
}

func voucherState(s *memory.Store) gen.VoucherState {
	for _, v := range s.World().Vouchers {
		if v.Id == voucherLive {
			return v.State
		}
	}
	return ""
}

// TestDisconnectSessionEndsItAndRecordsIt is the happy path, and its point is
// the last two assertions: the device was asked, and the log says who asked.
func TestDisconnectSessionEndsItAndRecordsIt(t *testing.T) {
	r := disconnectRig(t)
	r.signIn(adminUser)

	resp, raw := r.disconnect(liveSession, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disconnect: %d %s", resp.StatusCode, raw)
	}
	var out gen.SessionDisconnectResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Disconnected || out.Failure != nil {
		t.Fatalf("a device that ended the session answered disconnected=%v failure=%v", out.Disconnected, out.Failure)
	}
	// Nothing was asked for beside the session, so nothing else went.
	if out.GrantsRevoked != 0 || out.VoucherRevoked || out.Voucher != nil {
		t.Errorf("an unasked withdrawal happened: %s", raw)
	}
	if got := voucherState(r.store); got != gen.VoucherStateInUse {
		t.Errorf("the voucher is %q after a plain disconnect, want in-use — revoking a guest's paid "+
			"access is a second decision the operator did not make", got)
	}
	if grantRevoked(t, r.store) {
		t.Error("the grant was revoked by a plain disconnect")
	}
	sess, _ := r.store.SessionByID(context.Background(), liveSession)
	if sess.State != gen.SessionStateEnded {
		t.Errorf("the fake device was never asked: the session is still %q", sess.State)
	}

	entries := worldAuditOf(r.store, gen.AuditKindSessionDisconnected)
	if len(entries) != 1 {
		t.Fatalf("%d session-disconnected entries, want 1", len(entries))
	}
	e := entries[0]
	if e.ActorUsername != adminUser || e.Subject == nil || e.Subject.Kind != gen.AuditSubjectKindSession ||
		e.Subject.Id != liveSession {
		t.Errorf("the entry does not name who disconnected what: %+v", e)
	}
	if e.SiteId == nil || *e.SiteId != testSite {
		t.Errorf("the entry carries site %v, want %q — a disconnect is a venue's change", e.SiteId, testSite)
	}
	if e.Params == nil || (*e.Params)["disconnected"] != true {
		t.Errorf("the entry does not record that the device confirmed: %+v", e.Params)
	}
}

// TestDisconnectSessionIsGatedOnSessionsManage: staff may (counter work, as in
// ClearPass and UniFi's hotspot operator), a role without the permission may not,
// and neither may nobody.
func TestDisconnectSessionIsGatedOnSessionsManage(t *testing.T) {
	t.Run("staff", func(t *testing.T) {
		r := disconnectRig(t)
		r.signIn(staffUser)
		if resp, raw := r.disconnect(liveSession, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("staff disconnect: %d %s", resp.StatusCode, raw)
		}
	})

	t.Run("a role that does not hold it", func(t *testing.T) {
		r := disconnectRig(t)
		w := r.store.World()
		w.Operators = append(w.Operators, gen.Operator{
			Id: "op-odd", Username: "odd.role", Role: gen.OperatorRole("auditor"), Enabled: true, CreatedAt: at(-1),
		})
		w.Credentials["op-odd"] = testSecret
		r.signIn("odd.role")
		resp, raw := r.disconnect(liveSession, map[string]any{"revokeVoucher": true})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("a role without sessions:manage answered %d %s, want 403", resp.StatusCode, raw)
		}
		var p struct {
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.Params["required"] != string(gen.SessionsManage) {
			t.Errorf("the refusal names %v as required, want %q — the gate is not reading this "+
				"operation's x-permission", p.Params["required"], gen.SessionsManage)
		}
		if voucherState(r.store) != gen.VoucherStateInUse || len(worldAuditOf(r.store, gen.AuditKindSessionDisconnected)) != 0 {
			t.Error("a refused caller changed something")
		}
	})

	t.Run("nobody", func(t *testing.T) {
		r := disconnectRig(t)
		if resp, raw := r.disconnect(liveSession, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("no session answered %d %s, want 401", resp.StatusCode, raw)
		}
	})

	t.Run("no CSRF token", func(t *testing.T) {
		r := disconnectRig(t)
		r.signIn(adminUser)
		r.csrf = ""
		if resp, raw := r.disconnect(liveSession, nil); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("no CSRF token answered %d %s, want 403", resp.StatusCode, raw)
		}
	})
}

// TestDisconnectRefusalsChangeNothing: an ended session and a device that cannot
// end one are both refused **before** the voucher or the grant is touched, even
// when both were asked for.
func TestDisconnectRefusalsChangeNothing(t *testing.T) {
	cases := []struct {
		name, session string
		want          gen.ProblemType
		params        map[string]any
	}{
		{"ended", endedSession, gen.ProblemssessionNotLive, nil},
		{"no driver can", omadaSession, gen.ProblemsdisconnectUnsupported,
			map[string]any{"driver": "omada", "nasName": "Back yard"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := disconnectRig(t)
			ended := false
			r.store.EndSessionErr = func(string) error { ended = true; return nil }
			r.signIn(adminUser)

			resp, raw := r.disconnect(tc.session, map[string]any{"revokeVoucher": true, "revokeGrant": true})
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("answered %d %s, want 409", resp.StatusCode, raw)
			}
			var p struct {
				Type   gen.ProblemType `json:"type"`
				Params map[string]any  `json:"params"`
			}
			_ = json.Unmarshal(raw, &p)
			if p.Type != tc.want {
				t.Errorf("problem %q, want %q", p.Type, tc.want)
			}
			for k, v := range tc.params {
				if p.Params[k] != v {
					t.Errorf("params[%s] = %v, want %v", k, p.Params[k], v)
				}
			}
			if ended {
				t.Error("the device was asked although the call was refused")
			}
			if voucherState(r.store) != gen.VoucherStateInUse {
				t.Error("the voucher was revoked by a refused call")
			}
			if grantRevoked(t, r.store) {
				t.Error("the grant was revoked by a refused call")
			}
			if n := len(worldAuditOf(r.store, gen.AuditKindSessionDisconnected)); n != 0 {
				t.Errorf("%d audit entries for a call that changed nothing", n)
			}
		})
	}
}

// TestDisconnectTheDriverOverTheColumn is the driver-over-column rule for this
// operation: the stored capability says the device can, the driver says it
// cannot, and the driver wins.
func TestDisconnectTheDriverOverTheColumn(t *testing.T) {
	r := disconnectRig(t)
	r.store.CanEndSessionErr = func(string) error {
		return &api.DisconnectRefusal{Driver: gen.NasDriverKindRuckus, NasName: "Atrium"}
	}
	r.signIn(adminUser)
	resp, raw := r.disconnect(liveSession, map[string]any{"revokeVoucher": true})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("answered %d %s, want 409 disconnect-unsupported", resp.StatusCode, raw)
	}
	if voucherState(r.store) != gen.VoucherStateInUse {
		t.Error("the voucher was revoked on a device whose driver cannot end the session")
	}
}

// TestDisconnectWithdrawsBeforeItAsksTheDevice is the order, and a device that
// refused.
//
// The fake device looks at the store **at the moment it is asked**: if the voucher
// and the grant are not already withdrawn then, the order is the wrong one — the
// window in which a RADIUS client re-authenticates with the voucher it still holds.
func TestDisconnectWithdrawsBeforeItAsksTheDevice(t *testing.T) {
	r := disconnectRig(t)
	var atTheDevice struct {
		voucher gen.VoucherState
		grant   bool
	}
	r.store.EndSessionErr = func(string) error {
		atTheDevice.voucher = voucherState(r.store)
		atTheDevice.grant = grantRevoked(t, r.store)
		return &api.SessionEndError{Cause: api.SessionEndRefused, ErrorCause: 503, HasErrorCause: true}
	}
	r.signIn(adminUser)

	resp, raw := r.disconnect(liveSession, map[string]any{"revokeVoucher": true, "revokeGrant": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("answered %d %s, want 200 carrying the failure", resp.StatusCode, raw)
	}
	if atTheDevice.voucher != gen.VoucherStateRevoked || !atTheDevice.grant {
		t.Errorf("when the device was asked the voucher was %q and the grant revoked=%v; both must be "+
			"withdrawn first", atTheDevice.voucher, atTheDevice.grant)
	}

	var out gen.SessionDisconnectResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Disconnected {
		t.Fatal("a NAK was reported as a disconnect")
	}
	if out.Failure == nil || out.Failure.Type != gen.ProblemssessionEndFailed || out.Failure.Params == nil {
		t.Fatalf("no session-end-failed failure: %s", raw)
	}
	params := *out.Failure.Params
	if params["cause"] != "refused" || params["errorCause"] != float64(503) {
		t.Errorf("failure params %v, want cause=refused errorCause=503 — the operator's next step "+
			"depends on which failure it was", params)
	}
	if params["nasName"] != "Front of house" {
		t.Errorf("failure names device %v", params["nasName"])
	}
	if out.Failure.Detail != nil {
		t.Error("the failure carries a server-composed sentence")
	}
	if !out.VoucherRevoked || out.Voucher == nil || out.Voucher.State != gen.VoucherStateRevoked {
		t.Errorf("the answer does not report the voucher it revoked: %s", raw)
	}
	if out.GrantsRevoked != 1 {
		t.Errorf("grantsRevoked = %d, want 1", out.GrantsRevoked)
	}

	entries := worldAuditOf(r.store, gen.AuditKindSessionDisconnected)
	if len(entries) != 1 {
		t.Fatalf("%d entries for an attempt that revoked a voucher, want 1", len(entries))
	}
	p := *entries[0].Params
	if p["disconnected"] != false || p["voucherRevoked"] != true || p["grantsRevoked"] != 1 || p["voucherId"] != voucherLive {
		t.Errorf("the entry does not say what happened: %v", p)
	}
}

// TestDisconnectFailureCausesAreTheContractsWords: each typed failure reaches
// the console as its own word, and an untyped one as `error` with no text.
func TestDisconnectFailureCausesAreTheContractsWords(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{&api.SessionEndError{Cause: api.SessionEndNoAnswer}, "no-answer"},
		{&api.SessionEndError{Cause: api.SessionEndBadSecret}, "bad-secret"},
		{context.DeadlineExceeded, "error"},
	} {
		t.Run(c.want, func(t *testing.T) {
			r := disconnectRig(t)
			r.store.EndSessionErr = func(string) error { return c.err }
			r.signIn(adminUser)
			_, raw := r.disconnect(liveSession, nil)
			var out gen.SessionDisconnectResult
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
			if out.Disconnected || out.Failure == nil || (*out.Failure.Params)["cause"] != c.want {
				t.Fatalf("answer %s, want cause %q", raw, c.want)
			}
		})
	}
}
