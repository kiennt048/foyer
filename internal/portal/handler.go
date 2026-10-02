package portal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kiennt048/foyer/internal/nas"
)

// MediaTypeCaptiveJSON is the IETF RFC 8908 specified media type for captive portal JSON.
const MediaTypeCaptiveJSON = "application/captive+json"

// NegotiateLanguage selects the best matching language from Accept-Language header.
func NegotiateLanguage(acceptLang string, available []string, defaultLang string) string {
	if len(available) == 0 {
		return defaultLang
	}

	if acceptLang == "" {
		return available[0]
	}

	availMap := make(map[string]bool)
	for _, l := range available {
		availMap[strings.ToLower(l)] = true
	}

	// Simple RFC 7231 quality-aware parser
	tags := strings.Split(acceptLang, ",")
	for _, tag := range tags {
		parts := strings.Split(strings.TrimSpace(tag), ";")
		lang := strings.ToLower(strings.TrimSpace(parts[0]))

		if availMap[lang] {
			return lang
		}

		// Check primary subtag (e.g. "en-US" -> "en")
		if idx := strings.Index(lang, "-"); idx != -1 {
			primary := lang[:idx]
			if availMap[primary] {
				return primary
			}
		}
	}

	return available[0]
}

// RootRedirectHandler returns a handler that negotiates language and preserves query parameters across redirect.
func RootRedirectHandler(availableLangs []string, defaultLang string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := NegotiateLanguage(r.Header.Get("Accept-Language"), availableLangs, defaultLang)

		targetURL := fmt.Sprintf("/%s/", lang)
		if r.URL.RawQuery != "" {
			targetURL = fmt.Sprintf("/%s/?%s", lang, r.URL.RawQuery)
		}

		http.Redirect(w, r, targetURL, http.StatusFound)
	}
}

// GrantRequest is the payload submitted by a guest login form.
type GrantRequest struct {
	Method      string `json:"method"` // voucher, account, click_through
	ClientMAC   string `json:"clientMac"`
	APMAC       string `json:"apMac"`
	SiteID      string `json:"siteId"`
	VoucherCode string `json:"voucherCode,omitempty"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
}

// GrantHandler processes guest login, verifies presence, and authorizes client.
//
// ── not an entry point ───────────────────────────────────────────────────────
//
// The contract defines the portal's authenticating operations as `redeemVoucher`,
// `acceptTerms` and `loginWithAccount`, and they are implemented in
// `internal/api` against this package's `Service`. This route takes the client
// MAC **from the request body**, which means it has no browser-to-MAC binding at
// all and no session behind it, so `cmd/foyer` does not mount it (CONTRACT.md,
// "Not entry points").
//
// It still behaves correctly where it is called:
//
//   - **A nil driver is a refusal, not a skipped `VerifyPresence`.** A guard
//     reading `if driver != nil` would answer `303 authorized` to any MAC anybody
//     typed whenever the process had failed to build a driver. That is the exact
//     shape §4 exists to forbid, and it is why `Service` refuses to construct
//     without a driver rather than tolerating one.
//   - **Three different situations are not one "Device not present on
//     network".** A guest who is already authorized and a controller that is
//     unreachable each get their own answer. See `presence.go`.
func GrantHandler(driver nas.NASDriver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		var req GrantRequest
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "Bad Request", http.StatusBadRequest)
				return
			}
		} else {
			// Form POST from guest portal MPA
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Bad Request", http.StatusBadRequest)
				return
			}
			req = GrantRequest{
				Method:      r.FormValue("method"),
				ClientMAC:   r.FormValue("clientMac"),
				APMAC:       r.FormValue("apMac"),
				SiteID:      r.FormValue("siteId"),
				VoucherCode: r.FormValue("voucherCode"),
				Username:    r.FormValue("username"),
				Password:    r.FormValue("password"),
			}
		}

		if req.ClientMAC == "" {
			http.Error(w, "clientMac is required", http.StatusUnprocessableEntity)
			return
		}

		// INVARIANT 1: VerifyPresence is mandatory before any grant.
		//
		// Unconditional. A nil driver is a refusal, not a skip: the process has
		// nothing that can confirm a MAC, so it has nothing that may grant one.
		if driver == nil {
			http.Error(w, "This portal has no driver and cannot confirm a device.", http.StatusServiceUnavailable)
			return
		}

		verdict := verifyThrough(r, driver, req)
		switch verdict {
		case VerdictAlreadyOn:
			// Already on the network. Not an error, and not a second grant: the
			// guest is sent to the connected page.
			http.Redirect(w, r, Connected("en"), http.StatusSeeOther)
			return
		case VerdictUnavailable:
			// The venue's controller, not the guest's device. `502` says the
			// upstream failed; `403` would have blamed the person holding the
			// phone for an outage they cannot see and cannot act on.
			http.Error(w, "The network controller could not be reached. Try again shortly.", http.StatusBadGateway)
			return
		case VerdictRefuse:
			http.Error(w, "This device is not associated with this network.", http.StatusForbidden)
			return
		}

		// Authorize on controller / NAS
		authReq := nas.AuthorizeRequest{
			ClientMAC:        req.ClientMAC,
			APMAC:            req.APMAC,
			SiteID:           req.SiteID,
			TimeLimitMinutes: 120, // Default grant
		}
		if err := driver.AuthorizeGuest(r.Context(), authReq); err != nil {
			http.Error(w, "Failed to authorize guest on controller", http.StatusBadGateway)
			return
		}

		// MPA standard flow: 303 Redirect to connected status page
		if !strings.HasPrefix(r.Header.Get("Accept"), "application/json") {
			http.Redirect(w, r, "/en/connected/", http.StatusSeeOther)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "authorized",
			"mac":    req.ClientMAC,
		})
	}
}

// verifyThrough asks the driver and classifies the answer.
//
// It reaches for `PresenceDetailer` where the driver implements it and falls back
// to the two-value `VerifyPresence` where it does not. The fallback is honest
// about what it loses: a driver that cannot say *why* can only ever produce
// `VerdictProceed` or `VerdictRefuse`, so an outage behind such a driver still
// reads as a refusal. That is a property of the driver rather than of this code,
// and the fix is to implement the optional interface — which UniFi does.
func verifyThrough(r *http.Request, driver nas.NASDriver, req GrantRequest) Verdict {
	if d, ok := driver.(nas.PresenceDetailer); ok {
		p, err := d.VerifyPresenceDetail(r.Context(), req.ClientMAC, req.APMAC, req.SiteID)
		return Classify(p, err)
	}
	ok, err := driver.VerifyPresence(r.Context(), req.ClientMAC, req.APMAC, req.SiteID)
	if err != nil {
		return VerdictUnavailable
	}
	if !ok {
		return VerdictRefuse
	}
	return VerdictProceed
}
