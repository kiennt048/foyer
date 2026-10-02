package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestThePortalFallbackNeverClaimsAnUnknownPath drives **both** states of the
// portal's catch-all, because a tree is only ever in one of them and the one
// nobody was in answered `503` for `/de/`. The stand-in answers `200` so that a
// path reaching it is unmistakable.
func TestThePortalFallbackNeverClaimsAnUnknownPath(t *testing.T) {
	standIn := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, tc := range []struct {
		name  string
		built bool
		want  int
	}{
		{"frontend embedded: the tree decides", true, http.StatusOK},
		{"no frontend embedded: an unknown path is a 404", false, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mountPortalFallback(mux, tc.built, standIn)
			for _, p := range []string{"/de/", "/favicon.ico", "/zz/terms/"} {
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
				if rec.Code != tc.want {
					t.Errorf("GET %s answered %d, want %d", p, rec.Code, tc.want)
				}
			}
		})
	}
}
