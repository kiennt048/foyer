package sms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeESMS is eSMS as its documentation describes it: JSON in, `CodeResult` out.
type fakeESMS struct {
	mu     sync.Mutex
	paths  []string
	bodies []map[string]any
	code   string
	status int
}

func (f *fakeESMS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.paths = append(f.paths, r.URL.Path)
	f.bodies = append(f.bodies, body)
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	fmt.Fprintf(w, `{"CodeResult":%q,"SMSID":"x","ErrorMessage":"secret-ish text +84912345678"}`, f.code)
}

func TestESMSSendsTheDocumentedBrandnameRequest(t *testing.T) {
	f := &fakeESMS{code: "100"}
	srv := httptest.NewServer(f)
	defer srv.Close()

	p, err := New(Config{Kind: KindESMSSMS, APIKey: "k", SecretKey: "s", Brandname: "FOYER", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Send(context.Background(), Message{To: "+84912345678", Text: "123456 la ma dang nhap", RequestID: "otp_1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := f.paths[0]; got != "/SendMultipleMessage_V4_post_json/" {
		t.Fatalf("path %q", got)
	}
	b := f.bodies[0]
	want := map[string]any{
		"ApiKey": "k", "SecretKey": "s", "Phone": "0912345678", "Content": "123456 la ma dang nhap",
		"Brandname": "FOYER", "SmsType": "2", "IsUnicode": "0", "Sandbox": "0", "RequestId": "otp_1",
	}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("%s = %v, want %v", k, b[k], v)
		}
	}
}

func TestESMSZaloSendsTheCodeAsTheTemplateParameter(t *testing.T) {
	f := &fakeESMS{code: "100"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	p, err := New(Config{Kind: KindESMSZalo, APIKey: "k", SecretKey: "s", ZaloOAID: "oa", ZaloTemplateID: "t1", Sandbox: true, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Send(context.Background(), Message{To: "+84912345678", Code: "654321"}); err != nil {
		t.Fatal(err)
	}
	if f.paths[0] != "/SendZaloMessage_V5_post/" {
		t.Fatalf("path %q", f.paths[0])
	}
	b := f.bodies[0]
	if b["OAID"] != "oa" || b["TempID"] != "t1" || b["Sandbox"] != "1" {
		t.Fatalf("body %v", b)
	}
	if td, _ := b["TempData"].(map[string]any); td["otp"] != "654321" {
		t.Fatalf("TempData %v", b["TempData"])
	}
}

func TestESMSRefusalNamesTheResultCodeAndNothingElse(t *testing.T) {
	f := &fakeESMS{code: "146"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	p, _ := New(Config{Kind: KindESMSSMS, APIKey: "apikey-value", SecretKey: "secret-value", Brandname: "B", BaseURL: srv.URL})
	err := p.Send(context.Background(), Message{To: "+84912345678", Text: "999999 code"})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err %v, want ErrRefused", err)
	}
	for _, leak := range []string{"apikey-value", "secret-value", "912345678", "999999", "secret-ish"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error %q carries %q", err, leak)
		}
	}
}

func TestESMSDuplicateRequestIsTheFirstSendSucceeding(t *testing.T) {
	f := &fakeESMS{code: "124"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	p, _ := New(Config{Kind: KindESMSSMS, APIKey: "k", SecretKey: "s", Brandname: "B", BaseURL: srv.URL})
	if err := p.Send(context.Background(), Message{To: "+84912345678", Text: "x"}); err != nil {
		t.Fatalf("124 should read as accepted, got %v", err)
	}
}

func TestESMSServerErrorIsUnreachable(t *testing.T) {
	f := &fakeESMS{status: 503}
	srv := httptest.NewServer(f)
	defer srv.Close()
	p, _ := New(Config{Kind: KindESMSSMS, APIKey: "k", SecretKey: "s", Brandname: "B", BaseURL: srv.URL})
	if err := p.Send(context.Background(), Message{To: "+84912345678", Text: "x"}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err %v", err)
	}
}

func TestUnicodeFlagFollowsTheText(t *testing.T) {
	if unicodeFlag("123456 là mã") != "1" || unicodeFlag("123456 la ma") != "0" {
		t.Fatal("unicode flag")
	}
}

func TestConfigNeverPrintsItsKeys(t *testing.T) {
	c := Config{Kind: KindESMSSMS, APIKey: "apikey-value", SecretKey: "secret-value"}
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c)} {
		if strings.Contains(s, "apikey-value") || strings.Contains(s, "secret-value") {
			t.Fatalf("printed %q", s)
		}
	}
}

func TestNewCannotBuildTheFake(t *testing.T) {
	if _, err := New(Config{Kind: "fake"}); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("err %v", err)
	}
}
