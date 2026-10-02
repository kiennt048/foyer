package api_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// The console's settings screens put every technical knob behind "Advanced",
// each with its range stated, its default, and a "reset to default". Those three figures are the server's to send — the console
// compiles none of them in — so each test below proves two things at the HTTP
// boundary: the figure is sent, and **the figure sent is the one the server
// enforces**. A stated range the server does not hold would be a form that invites
// a refusal, and a "default" that is not the shipped value would reset to the wrong
// thing — which is exactly what one `default` for four different throttles did.

// TestEveryThrottleArrivesWithItsOwnShippedDefault — `bounds.throttleBurst.default`
// is one figure for four buckets; `defaults` is the figure per bucket.
func TestEveryThrottleArrivesWithItsOwnShippedDefault(t *testing.T) {
	r := newNetRig(t)
	r.signIn(adminUser)
	s := r.settings()
	if !reflect.DeepEqual(s.Defaults.Throttles, api.DefaultThrottles()) {
		t.Errorf("defaults.throttles = %+v, the shipped buckets are %+v", s.Defaults.Throttles, api.DefaultThrottles())
	}
	if !reflect.DeepEqual(s.Defaults.BodyCeilings, api.DefaultBodyCeilings()) {
		t.Errorf("defaults.bodyCeilings = %+v, shipped %+v", s.Defaults.BodyCeilings, api.DefaultBodyCeilings())
	}
	// The reason the member exists: the buckets do not share a default.
	if s.Defaults.Throttles.ConsoleAccount == s.Defaults.Throttles.PortalAddress {
		t.Fatalf("the per-account console bucket and the per-address portal bucket ship alike; this test proves nothing")
	}

	// A value moved away from its default and then set back to `defaults` reads back
	// as the default — the console's "reset" is a plain write of this figure.
	code, body := r.put(func(in *gen.NetworkSettingsUpdate) { in.Throttles.ConsoleAccount.Burst = 2 })
	if code != http.StatusOK {
		t.Fatalf("moving a throttle: %d %s", code, body)
	}
	code, body = r.put(func(in *gen.NetworkSettingsUpdate) { in.Throttles.ConsoleAccount = s.Defaults.Throttles.ConsoleAccount })
	if code != http.StatusOK {
		t.Fatalf("resetting it: %d %s", code, body)
	}
	if got := r.settings().Throttles.ConsoleAccount; got != s.Defaults.Throttles.ConsoleAccount {
		t.Errorf("after the reset the bucket is %+v, the default %+v", got, s.Defaults.Throttles.ConsoleAccount)
	}
}

// TestTheCoaRangeSentIsTheRangeEnforced — the console's timing inputs carry the
// server's range; one past either end is refused and each end itself is not.
func TestTheCoaRangeSentIsTheRangeEnforced(t *testing.T) {
	r := newNetRig(t)
	r.signIn(adminUser)
	var list struct {
		Defaults gen.CoaTimingValues `json:"defaults"`
		Bounds   gen.CoaTimingBounds `json:"bounds"`
	}
	resp := r.do(http.MethodGet, "/network/coa-timing", nil)
	if err := json.Unmarshal([]byte(readAll(t, resp)), &list); err != nil {
		t.Fatal(err)
	}
	b := list.Bounds
	if b.TimeoutMs.Maximum <= b.TimeoutMs.Minimum || b.Retries.Maximum <= b.Retries.Minimum {
		t.Fatalf("no range sent: %+v", b)
	}
	if b.TimeoutMs.Default != list.Defaults.TimeoutMs || b.Retries.Default != list.Defaults.Retries {
		t.Errorf("bounds say default %d ms × %d, defaults say %+v", b.TimeoutMs.Default, b.Retries.Default, list.Defaults)
	}
	put := func(ms, retries int) (int, string) {
		resp := r.do(http.MethodPut, "/nas/nas-coa/coa-timing", map[string]any{"timeoutMs": ms, "retries": retries})
		return resp.StatusCode, readAll(t, resp)
	}
	for _, c := range []struct {
		ms, retries int
		field       string
		want        gen.FieldFault
	}{
		{b.TimeoutMs.Minimum - 1, b.Retries.Default, "timeoutMs", gen.FieldFaultBelowMinimum},
		{b.TimeoutMs.Maximum + 1, b.Retries.Default, "timeoutMs", gen.FieldFaultAboveMaximum},
		{b.TimeoutMs.Default, b.Retries.Minimum - 1, "retries", gen.FieldFaultBelowMinimum},
		{b.TimeoutMs.Default, b.Retries.Maximum + 1, "retries", gen.FieldFaultAboveMaximum},
	} {
		code, body := put(c.ms, c.retries)
		if f, _ := faultOf(t, body, c.field); code != http.StatusUnprocessableEntity || f != c.want {
			t.Errorf("%d ms × %d: %d %s, want %s on %s", c.ms, c.retries, code, body, c.want, c.field)
		}
	}
	for _, c := range [][2]int{{b.TimeoutMs.Minimum, b.Retries.Minimum}, {b.TimeoutMs.Maximum, b.Retries.Maximum}} {
		if code, body := put(c[0], c[1]); code != http.StatusOK {
			t.Errorf("%d ms × %d is inside the range sent and was refused: %d %s", c[0], c[1], code, body)
		}
	}
}

// TestThePhoneAbuseRangesSentAreTheContractsAndHold — every limit's stated maximum
// is accepted and one more is refused, and the stated defaults are what a venue
// that never tuned anything is running.
func TestThePhoneAbuseRangesSentAreTheContractsAndHold(t *testing.T) {
	r, g := consoleAndPortal(t)
	configurePhone(t, g)
	path := "/sites/" + portalSite + "/portal/phone/abuse-controls"
	var c gen.PhoneAbuseControls
	if err := json.Unmarshal([]byte(mustStatus(t, r.do(http.MethodGet, path, nil), http.StatusOK)), &c); err != nil {
		t.Fatal(err)
	}
	if c.Customised {
		t.Fatalf("a fresh venue reads as customised")
	}
	b := c.Bounds
	bound := map[string]gen.NetworkBound{
		"perNumberPerHour": b.PerNumberPerHour, "perNumberPerDay": b.PerNumberPerDay,
		"perDevicePerHour": b.PerDevicePerHour, "perAddressPerHour": b.PerAddressPerHour,
		"codeMinutes": b.CodeMinutes, "maxAttempts": b.MaxAttempts,
	}
	running := map[string]int{
		"perNumberPerHour": c.Limits.PerNumberPerHour, "perNumberPerDay": c.Limits.PerNumberPerDay,
		"perDevicePerHour": c.Limits.PerDevicePerHour, "perAddressPerHour": c.Limits.PerAddressPerHour,
		"codeMinutes": c.Limits.CodeMinutes, "maxAttempts": c.Limits.MaxAttempts,
	}
	at := func(pick func(gen.NetworkBound) int) map[string]any {
		out := map[string]any{}
		for k, v := range bound {
			out[k] = pick(v)
		}
		return out
	}
	for k, v := range bound {
		if v.Minimum < 1 || v.Maximum <= v.Minimum {
			t.Errorf("%s: no range sent: %+v", k, v)
		}
		if v.Default != running[k] {
			t.Errorf("%s: default stated %d, an untuned venue runs %d", k, v.Default, running[k])
		}
	}
	mustStatus(t, r.do(http.MethodPut, path, map[string]any{
		"limits": at(func(b gen.NetworkBound) int { return b.Maximum }), "mobilePrefixes": []string{"09"}}), http.StatusOK)
	for k := range bound {
		limits := at(func(b gen.NetworkBound) int { return b.Maximum })
		limits[k] = bound[k].Maximum + 1
		body := mustStatus(t, r.do(http.MethodPut, path, map[string]any{"limits": limits, "mobilePrefixes": []string{"09"}}),
			http.StatusUnprocessableEntity)
		if !strings.Contains(body, "limits."+k) && !strings.Contains(body, k) {
			t.Errorf("%s one past its stated maximum was refused, but not on that field: %s", k, body)
		}
	}

	var cap gen.PhoneSpendCap
	_ = json.Unmarshal([]byte(mustStatus(t, r.do(http.MethodGet, "/phone-sign-in/spend-cap", nil), http.StatusOK)), &cap)
	if cap.Bounds.Minimum < 1 || cap.Bounds.Maximum <= cap.Bounds.Minimum {
		t.Fatalf("spend cap: no range sent: %+v", cap.Bounds)
	}
	mustStatus(t, r.do(http.MethodPut, "/phone-sign-in/spend-cap", map[string]any{"dailyLimit": cap.Bounds.Maximum}), http.StatusOK)
	mustStatus(t, r.do(http.MethodPut, "/phone-sign-in/spend-cap", map[string]any{"dailyLimit": cap.Bounds.Maximum + 1}),
		http.StatusUnprocessableEntity)
	mustStatus(t, r.do(http.MethodPut, "/phone-sign-in/spend-cap", map[string]any{"dailyLimit": cap.Bounds.Minimum - 1}),
		http.StatusUnprocessableEntity)
}
