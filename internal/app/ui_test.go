package app

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestSCN_UI_009_PageNonce(t *testing.T) {
	h := uiHandler(&Deps{})
	meta := regexp.MustCompile(`<meta name="csp-nonce" content="([^"]+)"`)
	seen := map[string]bool{}
	for range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings/general", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: build the UI first (just ui-build)", rec.Code)
		}
		m := meta.FindStringSubmatch(rec.Body.String())
		if m == nil {
			t.Fatal("the page has no nonce meta tag")
		}
		if got, want := rec.Header().Get("Content-Security-Policy"), PageCSP(m[1]); got != want {
			t.Fatalf("CSP %q, want %q", got, want)
		}
		if seen[m[1]] {
			t.Fatal("two responses share a nonce")
		}
		seen[m[1]] = true
	}
}
