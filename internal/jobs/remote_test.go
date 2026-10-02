package jobs_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/kiennt048/foyer/internal/jobs"
	"github.com/kiennt048/foyer/internal/store"
)

// The maintenance container's door: the control plane holds neither
// maintenance login and asks this door for erasure's retention half. These tests
// hold its three properties: only a caller with the token gets in, the subject is
// rebuilt on this side from its kind and one value, and what the client sends
// arrives as the same subject.

const token = "0123456789abcdef0123456789abcdef0123456789abcdef"

type recordingEraser struct {
	mu   sync.Mutex
	got  []store.Subject
	resp map[string]int64
}

func (r *recordingEraser) EraseRetained(_ context.Context, s store.Subject) (map[string]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, s)
	return r.resp, nil
}

func door(t *testing.T, eraser store.RetentionEraser, changed func(context.Context)) *httptest.Server {
	t.Helper()
	h, err := jobs.NewServer(jobs.ServerConfig{Token: token, Retention: eraser, SettingsChanged: changed})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestTheMaintenanceDoorAdmitsOnlyTheToken(t *testing.T) {
	rec := &recordingEraser{}
	ts := door(t, rec, nil)
	for _, auth := range []string{"", "Bearer ", "Bearer " + token[:len(token)-1], "Bearer " + token + "x", token} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+jobs.MaintenanceErasePath, strings.NewReader(`{"kind":"mac","value":"aa:bb:cc:dd:ee:ff"}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Authorization %q answered %d, want 401", auth, resp.StatusCode)
		}
	}
	if len(rec.got) != 0 {
		t.Fatalf("an unauthenticated request erased %d subject(s)", len(rec.got))
	}
	// The healthcheck is the one open answer, and it says nothing.
	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz answered %d", resp.StatusCode)
	}
	if _, err := jobs.NewServer(jobs.ServerConfig{Token: "short"}); err == nil {
		t.Fatal("a door with a guessable token was built")
	}
	if _, err := jobs.NewRemoteClient("http://x", "short"); err == nil {
		t.Fatal("a client with a guessable token was built")
	}
}

// TestAnErasureCrossesTheDoorAsTheSameSubject: the control plane's Subject
// travels as its kind and one value and is rebuilt by the store's own parsers on
// the other side, and the counts come back.
func TestAnErasureCrossesTheDoorAsTheSameSubject(t *testing.T) {
	rec := &recordingEraser{resp: map[string]int64{"radacct": 3, "radpostauth": 1}}
	ts := door(t, rec, nil)
	c, err := jobs.NewRemoteClient(ts.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	mac, _ := store.MACSubject("AA-BB-CC-DD-EE-FF")
	vn, _ := store.PhoneSubject("0912 345 678")
	intl, _ := store.PhoneSubject("+1 (415) 555-0100")
	for _, s := range []store.Subject{mac, vn, intl} {
		got, err := c.EraseRetained(context.Background(), s)
		if err != nil {
			t.Fatalf("%s: %v", s.Kind, err)
		}
		if got["radacct"] != 3 || got["radpostauth"] != 1 {
			t.Fatalf("%s: counts %v did not come back", s.Kind, got)
		}
		last := rec.got[len(rec.got)-1]
		if !reflect.DeepEqual(last, s) {
			t.Fatalf("the door erased %+v, the control plane named %+v", last, s)
		}
	}
}

// TestTheDoorRebuildsTheSubjectAndRefusesWhatIsNotOne: a caller cannot hand the
// door a built subject — only a kind and a value the store's parsers accept.
func TestTheDoorRebuildsTheSubjectAndRefusesWhatIsNotOne(t *testing.T) {
	rec := &recordingEraser{}
	ts := door(t, rec, nil)
	for _, body := range []string{
		`{"kind":"mac","value":"cafe-0011-2233-44 extra"}`,
		`{"kind":"mac","value":"aa:bb:cc:dd:ee:ff","MACHex":"%"}`,
		`{"kind":"phone","value":"12"}`,
		`{"kind":"account","value":"acct-1"}`, // no application connection here
		`{"kind":"everything"}`,
		`not json`,
	} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+jobs.MaintenanceErasePath, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if body == `{"kind":"mac","value":"aa:bb:cc:dd:ee:ff","MACHex":"%"}` {
			// An extra member is ignored, not obeyed: the subject is rebuilt.
			if resp.StatusCode != http.StatusOK || rec.got[len(rec.got)-1].MACHex != "aabbccddeeff" {
				t.Fatalf("a request carrying its own MACHex answered %d and erased %+v", resp.StatusCode, rec.got)
			}
			continue
		}
		if resp.StatusCode < 400 {
			t.Fatalf("%s answered %d, want a refusal", body, resp.StatusCode)
		}
	}
}

func TestASettingsChangeReachesTheScheduler(t *testing.T) {
	called := make(chan struct{}, 1)
	ts := door(t, &recordingEraser{}, func(context.Context) { called <- struct{}{} })
	c, err := jobs.NewRemoteClient(ts.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	c.SettingsChanged(context.Background(), nil)
	select {
	case <-called:
	default:
		t.Fatal("the maintenance container was not told the settings changed")
	}
}
