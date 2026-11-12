package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/alternayte/sayso/internal/auth"
)

func TestSCN_CORE_012_TemporalUIAdminsOnly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("the proxy forwarded the session cookie")
		}
		_, _ = w.Write([]byte("temporal ui " + r.URL.Path))
	}))
	defer upstream.Close()
	h := temporalUI(upstream.URL)
	for _, c := range []struct {
		role auth.Role
		want int
	}{{auth.Admin, 200}, {auth.Maintainer, 404}, {auth.Viewer, 404}, {"", 404}} {
		req := httptest.NewRequest(http.MethodGet, "/temporal/namespaces/default/workflows", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "x"})
		if c.role != "" {
			req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: uuid.New(), Login: "u", Role: c.role}))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("role %q: %d, want %d", c.role, rec.Code, c.want)
		}
		if c.want == 200 && rec.Body.String() != "temporal ui /temporal/namespaces/default/workflows" {
			t.Errorf("admin body %q", rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/temporal/", nil)
	temporalUI("").ServeHTTP(rec, req.WithContext(auth.WithUser(req.Context(), &auth.User{Role: auth.Admin})))
	if rec.Code != 404 {
		t.Errorf("no UI configured: %d, want 404", rec.Code)
	}
}
