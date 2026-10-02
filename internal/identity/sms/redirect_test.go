package sms

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The keys are in the request body, and
// `net/http` follows a 307/308 by re-sending that body to the new location, on any
// host. Before the adapter refused redirects, an answer of
// `307 Location: https://elsewhere/` delivered the venue's ApiKey and SecretKey to
// elsewhere; this test is red on that adapter.
func TestESMSDoesNotFollowARedirectWithTheKeys(t *testing.T) {
	var leaked atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "SECRET-KEY") {
			leaked.Store(true)
		}
		_, _ = io.WriteString(w, `{"CodeResult":"100"}`)
	}))
	defer elsewhere.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer provider.Close()

	for _, kind := range []string{KindESMSSMS, KindESMSZalo} {
		p, err := New(Config{Kind: kind, APIKey: "API-KEY", SecretKey: "SECRET-KEY", Brandname: "FOYER",
			ZaloOAID: "oa", ZaloTemplateID: "tpl", BaseURL: provider.URL})
		if err != nil {
			t.Fatal(err)
		}
		err = p.Send(context.Background(), Message{To: "+84912345678", Code: "123456", Text: "123456 la ma", RequestID: "otp_r"})
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: a redirect was read as %v, want a refusal", kind, err)
		}
	}
	if leaked.Load() {
		t.Fatal("the provider's keys were re-sent to the host a redirect named")
	}
}
