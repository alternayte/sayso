package app

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/alternayte/sayso/internal/auth"
)

// temporalUI proxies /temporal/* to the read-only Temporal UI for admins.
// Everyone else, and every request when no UI is configured, gets 404 (D-21).
func temporalUI(target string) http.Handler {
	var proxy *httputil.ReverseProxy
	if target != "" {
		if u, err := url.Parse(target); err == nil {
			proxy = &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(u)
				r.Out.URL.Path = r.In.URL.Path
				r.Out.URL.RawPath = r.In.URL.RawPath
				r.Out.Header.Del("Cookie")
			}}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFrom(r.Context())
		if proxy == nil || u == nil || u.Role != auth.Admin {
			http.NotFound(w, r)
			return
		}
		// The Temporal UI sets its own policy for its pages.
		w.Header().Del("Content-Security-Policy")
		proxy.ServeHTTP(w, r)
	})
}
