package portal

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/nas"
)

func TestNegotiateLanguage(t *testing.T) {
	langs := []string{"en", "vi"}

	tests := []struct {
		accept   string
		expected string
	}{
		{"vi,en;q=0.9", "vi"},
		{"en-US,en;q=0.8", "en"},
		{"fr,de;q=0.7", "en"}, // Fallback to first available
		{"", "en"},
	}

	for _, tt := range tests {
		got := NegotiateLanguage(tt.accept, langs, "en")
		if got != tt.expected {
			t.Errorf("NegotiateLanguage(%q) = %s, expected %s", tt.accept, got, tt.expected)
		}
	}
}

func TestRootRedirectHandler_PreservesQueryParams(t *testing.T) {
	handler := RootRedirectHandler([]string{"en", "vi"}, "en")

	req := httptest.NewRequest(http.MethodGet, "/?ap=94:2a:6f:d0:30:57&id=1c:71:25:63:e4:24&t=1742398732&ssid=guest", nil)
	req.Header.Set("Accept-Language", "vi")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected status 302 Found, got %d", w.Code)
	}

	loc := w.Header().Get("Location")
	expected := "/vi/?ap=94:2a:6f:d0:30:57&id=1c:71:25:63:e4:24&t=1742398732&ssid=guest"
	if loc != expected {
		t.Errorf("Location header = %q, expected %q", loc, expected)
	}
}

func TestGrantHandler_FormPost_303Redirect(t *testing.T) {
	driver := nas.NewMockDriver(nas.VendorUniFi)

	handler := GrantHandler(driver)

	form := url.Values{
		"method":      {"voucher"},
		"clientMac":   {"00:11:22:33:44:55"},
		"voucherCode": {"VOUCHER-123"},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/portal/grant", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected status 303 See Other, got %d", w.Code)
	}

	if w.Header().Get("Location") != "/en/connected/" {
		t.Errorf("expected redirect to /en/connected/, got %s", w.Header().Get("Location"))
	}

	if _, exists := driver.Authorized["00:11:22:33:44:55"]; !exists {
		t.Errorf("expected client to be authorized in driver")
	}
}
