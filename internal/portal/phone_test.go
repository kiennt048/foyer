package portal

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/nas"
)

// fakeCodes is the otp seam with one right code and a count of sends.
type fakeCodes struct {
	mu      sync.Mutex
	offered bool
	minutes int
	sends   int
	sendErr error
	right   string
	checks  int
	lastReq PhoneCodeRequest
}

func (f *fakeCodes) Offered(context.Context, string) (int, bool) { return f.minutes, f.offered }

func (f *fakeCodes) Send(_ context.Context, r PhoneCodeRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastReq = r
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sends++
	return nil
}

func (f *fakeCodes) Check(_ context.Context, _, code string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if code != f.right {
		return "", ErrCodeWrong
	}
	return "+84912345678", nil
}

func phoneRig(t *testing.T, presence nas.Presence, err error) (*rig, *fakeCodes) {
	t.Helper()
	r := newRig(t, presence, err)
	codes := &fakeCodes{offered: true, minutes: 90, right: "482913"}
	p := r.svc.Ports()
	p.PhoneCodes = codes
	svc, e := NewService(ServiceConfig{Ports: p, Now: func() time.Time { return r.now }, NewID: func() (string, error) { return "grant-1", nil }})
	if e != nil {
		t.Fatal(e)
	}
	r.svc = svc
	return r, codes
}

// TestNoCodeIsSentToADeviceTheDriverDidNotConfirm — the toll-fraud half of §4:
// a device the venue's controller cannot place on the network does not get to
// spend the venue's SMS budget. Asserted on the provider's send count, not on the
// redirect, because the redirect looks the same either way.
func TestNoCodeIsSentToADeviceTheDriverDidNotConfirm(t *testing.T) {
	for _, c := range []struct {
		name string
		p    nas.Presence
		err  error
	}{
		{"never associated", nas.Presence{Outcome: nas.PresenceNotFound}, nil},
		{"a different access point", nas.Presence{Outcome: nas.PresenceAPMismatch}, nil},
		{"the controller could not answer", nas.Presence{Outcome: nas.PresenceUnavailable}, errors.New("down")},
		{"already online", nas.Presence{Outcome: nas.PresenceAlreadyAuthorized}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, codes := phoneRig(t, c.p, c.err)
			out := r.svc.SendPhoneCode(context.Background(), r.session, "0912345678", "10.0.0.1")
			if out.Sent || codes.sends != 0 {
				t.Fatalf("a code was sent: %+v, %d sends", out, codes.sends)
			}
		})
	}
}

func TestPhoneSignInGrantsThePhoneMethodAndItsDuration(t *testing.T) {
	r, codes := phoneRig(t, present(), nil)
	ctx := context.Background()
	if out := r.svc.SendPhoneCode(ctx, r.session, "0912345678", "10.0.0.1"); !out.Sent {
		t.Fatalf("send: %+v", out)
	}
	if codes.lastReq.SessionTokenHash != r.session.TokenHash || codes.lastReq.ClientMAC != r.session.ClientMAC ||
		codes.lastReq.ClientIP != "10.0.0.1" {
		t.Fatalf("the code was not bound to this browser: %+v", codes.lastReq)
	}
	out := r.svc.PhoneLogin(ctx, r.session, "482913")
	if !out.Connected || out.Grant == nil {
		t.Fatalf("login: %+v", out)
	}
	g := r.store.Grants()[0]
	if g.Method != MethodPhone || g.Phone != "+84912345678" || g.Plan.DurationMinutes == nil || *g.Plan.DurationMinutes != 90 {
		t.Fatalf("grant %+v", g)
	}
	if err := out.Authorize(ctx); err != nil {
		t.Fatal(err)
	}
	if a := r.store.Authorizations(); len(a) != 1 || a[0].Username != "+84912345678" || a[0].Method != MethodPhone {
		t.Fatalf("authorization record %+v", a)
	}
}

func TestAWrongCodeGrantsNothing(t *testing.T) {
	r, _ := phoneRig(t, present(), nil)
	out := r.svc.PhoneLogin(context.Background(), r.session, "000000")
	if out.Connected || out.Reason != ReasonCodeWrong || len(r.store.Grants()) != 0 {
		t.Fatalf("%+v, %d grants", out, len(r.store.Grants()))
	}
}

func TestNoCodeIsSpentForADeviceNotOnTheNetwork(t *testing.T) {
	r, codes := phoneRig(t, nas.Presence{Outcome: nas.PresenceNotFound}, nil)
	out := r.svc.PhoneLogin(context.Background(), r.session, "482913")
	if out.Connected || codes.checks != 0 {
		t.Fatalf("%+v, %d checks", out, codes.checks)
	}
}

func TestPhoneNotOfferedAnswersNotOffered(t *testing.T) {
	r, codes := phoneRig(t, present(), nil)
	codes.offered = false
	if out := r.svc.SendPhoneCode(context.Background(), r.session, "0912345678", ""); !out.NotOffered {
		t.Fatalf("%+v", out)
	}
	if out := r.svc.PhoneLogin(context.Background(), r.session, "482913"); !out.NotOffered {
		t.Fatalf("%+v", out)
	}
	// And with no port at all.
	plain := newRig(t, present(), nil)
	if out := plain.svc.SendPhoneCode(context.Background(), plain.session, "0912345678", ""); !out.NotOffered {
		t.Fatalf("%+v", out)
	}
}

func TestPhoneSendFailuresMapToCopy(t *testing.T) {
	for err, want := range map[error]Reason{
		ErrPhoneInvalid:    ReasonPhoneInvalid,
		ErrPhoneLimited:    ReasonCodeLimit,
		ErrPhoneCannotSend: ReasonCodeUnavailable,
		errors.New("boom"): ReasonCodeUnavailable,
	} {
		r, codes := phoneRig(t, present(), nil)
		codes.sendErr = err
		if out := r.svc.SendPhoneCode(context.Background(), r.session, "x", ""); out.Reason != want {
			t.Errorf("%v: %+v, want %s", err, out, want)
		}
	}
}

// The redirect never carries the number, and a wrong code keeps the code form.
func TestPhoneRedirectsCarryNoNumber(t *testing.T) {
	for _, loc := range []string{PhoneSentPage("vi"), PhoneFailure("en", ReasonPhoneInvalid), PhoneFailure("en", ReasonCodeWrong)} {
		if strings.Contains(loc, "912") || !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "//") {
			t.Fatalf("location %q", loc)
		}
	}
	u, _ := url.Parse(PhoneFailure("en", ReasonCodeWrong))
	if u.Query().Get("sent") != "1" || u.Query().Get("e") != "code-wrong" {
		t.Fatalf("%v", u)
	}
	u, _ = url.Parse(PhoneFailure("en", ReasonCodeExpired))
	if u.Query().Get("sent") != "" {
		t.Fatalf("an expired code should return to the number form: %v", u)
	}
}

func TestThePhonePageRendersBothSteps(t *testing.T) {
	set := &RenderSettings{VenueName: "Cà phê", Methods: []string{"phone"}, Languages: []string{"vi", "en"}}
	welcome := RenderPage(RenderInput{Lang: "vi", Path: "", Settings: set})
	if !strings.Contains(welcome.Body, `href="/vi/phone/"`) || !strings.Contains(welcome.Body, catalogueVi["welcome.phone"]) {
		t.Fatal("the welcome page does not offer phone sign-in")
	}
	number := RenderPage(RenderInput{Lang: "vi", Path: "phone/", Settings: set, Error: "phone-invalid"})
	if number == nil || !strings.Contains(number.Body, `action="/api/v0/portal/phone"`) ||
		!strings.Contains(number.Body, catalogueVi["error.phone-invalid"]) || strings.Contains(number.Body, "portal/phone/code") {
		t.Fatalf("number step:\n%v", number)
	}
	code := RenderPage(RenderInput{Lang: "en", Path: "phone/", Settings: set, PhoneSent: true})
	if !strings.Contains(code.Body, `action="/api/v0/portal/phone/code"`) || !strings.Contains(code.Body, `autocomplete="one-time-code"`) {
		t.Fatalf("code step:\n%s", code.Body)
	}
}
