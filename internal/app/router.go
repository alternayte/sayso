package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/auth"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/secrets"
	"github.com/alternayte/sayso/internal/settings"
)

// Features are the services that the HTTP handler exposes.
type Features struct {
	Settings *settings.Store
	Secrets  *secrets.Store
	Auth     *auth.Service
}

// NewFeatures builds the feature services on the shared resources.
func NewFeatures(ctx context.Context, d *Deps) (*Features, error) {
	sec, err := secrets.Open(ctx, d.Pool, d.Config.SecretKey, d.Redactor, getenv)
	if err != nil {
		return nil, err
	}
	st := settings.NewStore(d.Pool)
	if err := st.SeedDefaults(ctx); err != nil {
		return nil, err
	}
	hc, err := HTTPClient(d.Config)
	if err != nil {
		return nil, err
	}
	gh := func(ctx context.Context) (*github.Client, error) {
		web, err := st.String(ctx, settings.GitHubWebURL)
		if err != nil {
			return nil, err
		}
		api, err := st.String(ctx, settings.GitHubAPIURL)
		if err != nil {
			return nil, err
		}
		return &github.Client{WebURL: web, APIURL: api, HTTP: hc}, nil
	}
	a := auth.NewService(d.Pool, auth.Config{
		BaseURL: d.Config.BaseURL, SetupToken: d.Config.SetupToken, BootstrapAdmins: d.Config.BootstrapAdmins,
		SigningKey: derive(d.Config.SecretKey, "setup cookie"),
	}, sec, gh, d.Log)
	return &Features{Settings: st, Secrets: sec, Auth: a}, nil
}

func derive(key []byte, purpose string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(purpose))
	return m.Sum(nil)
}

// NewHandler returns the HTTP handler of the api role.
func NewHandler(d *Deps, f *Features) http.Handler {
	r := chi.NewRouter()
	r.Use(httpMetrics(d.Metrics), securityHeaders, f.Auth.Middleware)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/readyz", d.readyz)
	r.Handle("/metrics", promhttp.HandlerFor(d.Metrics, promhttp.HandlerOpts{}))
	r.Get("/auth/github/login", f.Auth.Login)
	r.Get("/auth/github/callback", f.Auth.Callback)
	r.Post("/auth/logout", f.Auth.Logout)

	api := NewAPI(r)
	api.UseMiddleware(auth.Enforce(api, func(ctx context.Context) (bool, error) { return github.Exists(ctx, d.Pool) }))
	v1 := huma.NewGroup(api, "/api/v1")
	f.Auth.Register(v1)
	f.Settings.Register(v1)
	audit.Register(v1, d.Pool, auth.Op(auth.Admin))
	return r
}

// NewAPI creates the Huma API on a router. `sayso openapi` uses it to
// export the document.
func NewAPI(r chi.Router) huma.API {
	cfg := huma.DefaultConfig("Sayso API", "1.0.0")
	cfg.OpenAPIPath = "/api/v1/openapi"
	cfg.DocsPath = ""
	cfg.SchemasPath = "/api/v1/schemas"
	return humachi.New(r, cfg)
}

// securityHeaders sets the Content-Security-Policy of §16.4 on every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
