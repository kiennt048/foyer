package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// eSMS.vn, over plain `net/http` — `go.mod` is frozen and this needs nothing else.
//
// From the published API (developers.esms.vn):
//
//   - SMS brandname, customer-care type: `POST
//     https://rest.esms.vn/MainService.svc/json/SendMultipleMessage_V4_post_json/`
//     with `ApiKey`, `SecretKey`, `Phone`, `Content`, `Brandname`, `SmsType: "2"`,
//     `IsUnicode`, `Sandbox`, `RequestId`.
//   - Zalo ZNS: `POST .../SendZaloMessage_V5_post/` with `ApiKey`, `SecretKey`,
//     `Phone`, `OAID`, `TempID`, `TempData` (the template's parameters as an
//     object), `Sandbox`, `RequestId`.
//   - Both answer `{"CodeResult": "100", "SMSID": "…"}` on acceptance. Anything
//     else is a refusal: `101` wrong keys, `104` brandname not active, `146`
//     content does not match the registered CSKH template, `789` ZNS template not
//     configured, `99` malformed request, `124` duplicate `RequestId`.
//
// **`124` is read as success**, and it is the reason `RequestId` is sent at all: it
// means eSMS already accepted a message with this id — the first attempt of a send
// whose answer was lost — so the guest has, or will have, their code.
//
// UNVERIFIED against the live service; see the package comment.

const esmsBase = "https://rest.esms.vn/MainService.svc/json/"

// esmsTimeout bounds one send. The guest is waiting on a `303` for this; five
// seconds is already longer than a person watching a spinner in a captive window
// will tolerate, and a provider that has not answered by then is `unreachable`.
const esmsTimeout = 5 * time.Second

// ESMS sends an SMS from a registered brandname.
type ESMS struct {
	cfg    Config
	client *http.Client
}

// ESMSZalo sends a Zalo ZNS template message.
type ESMSZalo struct {
	cfg    Config
	client *http.Client
}

type esmsSMSRequest struct {
	APIKey    string `json:"ApiKey"`
	SecretKey string `json:"SecretKey"`
	Phone     string `json:"Phone"`
	Content   string `json:"Content"`
	Brandname string `json:"Brandname"`
	SmsType   string `json:"SmsType"`
	IsUnicode string `json:"IsUnicode"`
	Sandbox   string `json:"Sandbox"`
	RequestID string `json:"RequestId,omitempty"`
}

type esmsZaloRequest struct {
	APIKey    string            `json:"ApiKey"`
	SecretKey string            `json:"SecretKey"`
	Phone     string            `json:"Phone"`
	OAID      string            `json:"OAID"`
	TempID    string            `json:"TempID"`
	TempData  map[string]string `json:"TempData"`
	Sandbox   string            `json:"Sandbox"`
	RequestID string            `json:"RequestId,omitempty"`
}

type esmsResponse struct {
	CodeResult string `json:"CodeResult"`
}

// Send implements Provider.
func (e *ESMS) Send(ctx context.Context, m Message) error {
	body := esmsSMSRequest{
		APIKey:    e.cfg.APIKey,
		SecretKey: e.cfg.SecretKey,
		Phone:     nationalVN(m.To),
		Content:   m.Text,
		Brandname: e.cfg.Brandname,
		SmsType:   "2",
		IsUnicode: unicodeFlag(m.Text),
		Sandbox:   flag(e.cfg.Sandbox),
		RequestID: m.RequestID,
	}
	return post(ctx, e.client, base(e.cfg), "SendMultipleMessage_V4_post_json/", body)
}

// Send implements Provider.
func (z *ESMSZalo) Send(ctx context.Context, m Message) error {
	body := esmsZaloRequest{
		APIKey:    z.cfg.APIKey,
		SecretKey: z.cfg.SecretKey,
		Phone:     nationalVN(m.To),
		OAID:      z.cfg.ZaloOAID,
		TempID:    z.cfg.ZaloTemplateID,
		// The template's one parameter. `otp` is the name Zalo's own OTP template
		// category uses; a venue whose approved template names it differently has
		// to register one that does. UNVERIFIED.
		TempData:  map[string]string{"otp": m.Code},
		Sandbox:   flag(z.cfg.Sandbox),
		RequestID: m.RequestID,
	}
	return post(ctx, z.client, base(z.cfg), "SendZaloMessage_V5_post/", body)
}

func base(c Config) string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/") + "/"
	}
	return esmsBase
}

func post(ctx context.Context, client *http.Client, baseURL, path string, body any) error {
	if client == nil {
		client = &http.Client{Timeout: esmsTimeout, CheckRedirect: refuseRedirect}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: encoding the request", ErrRefused)
	}
	ctx, cancel := context.WithTimeout(ctx, esmsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: building the request", ErrRefused)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// The transport error is not wrapped: `url.Error` carries the URL, and a
		// future provider that takes its key in the query string would put the key
		// in this message.
		return ErrUnreachable
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%w: HTTP %d", ErrUnreachable, resp.StatusCode)
	}
	var out esmsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return fmt.Errorf("%w: HTTP %d with no result code", ErrRefused, resp.StatusCode)
	}
	switch out.CodeResult {
	case "100", "124":
		return nil
	default:
		// The result code only. eSMS's `ErrorMessage` is text somebody else wrote.
		return &Refusal{Provider: "eSMS", Code: sanitiseCode(out.CodeResult)}
	}
}

// refuseRedirect keeps the keys at the provider's address. The keys travel in the
// JSON body, and `net/http` re-sends a body on a `307`/`308` — to whatever host the
// `Location` names, stripping only the headers it knows are sensitive. eSMS's
// documented API answers `200` with a `CodeResult`; a redirect is not an answer, so
// it is read as one (no result code: refused) and never followed.
func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// unicodeFlag asks for a Unicode SMS only when the text needs one. A Vietnamese
// template written with diacritics is 70 characters a segment instead of 160, so it
// costs more, and whether the carrier accepts it is the template's registration.
func unicodeFlag(text string) string {
	for _, r := range text {
		if r > 0x7e {
			return "1"
		}
	}
	return "0"
}

func flag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// sanitiseCode keeps a result code a result code: at most eight alphanumerics.
func sanitiseCode(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 8 {
			break
		}
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
