package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/auth"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/mcpproxy"
	"github.com/alternayte/sayso/internal/models"
	"github.com/alternayte/sayso/internal/repos"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/storage"
	"github.com/alternayte/sayso/internal/secrets"
	"github.com/alternayte/sayso/internal/settings"
)

// Features are the services that the HTTP handler exposes.
type Features struct {
	Settings     *settings.Store
	Secrets      *secrets.Store
	Auth         *auth.Service
	GitHubClient func(ctx context.Context) (*github.Client, error)
	RateLimits   *github.RateLimits
	Tokens       *github.Tokens
	Ingest       *github.Ingest
	Relay        *github.RelayClient
	Models       *models.Service
	ProxyMetrics *models.ProxyMetrics
	MCP          *mcpproxy.Service
	MCPProxy     *mcpproxy.Proxy
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
	limits := github.NewRateLimits(github.RealClock)
	gh := func(ctx context.Context) (*github.Client, error) {
		web, err := st.String(ctx, settings.GitHubWebURL)
		if err != nil {
			return nil, err
		}
		api, err := st.String(ctx, settings.GitHubAPIURL)
		if err != nil {
			return nil, err
		}
		return &github.Client{WebURL: web, APIURL: api, HTTP: hc, Limits: limits}, nil
	}
	a := auth.NewService(d.Pool, auth.Config{
		BaseURL: d.Config.BaseURL, SetupToken: d.Config.SetupToken, BootstrapAdmins: d.Config.BootstrapAdmins,
		WebhookURL: d.Config.PublicWebhookURL,
		SigningKey: derive(d.Config.SecretKey, "setup cookie"),
	}, sec, gh, d.Log)
	signaled := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "sayso_webhook_signal_seconds", Help: "Time from webhook request start to the workflow signal.",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2, 5},
	})
	d.Metrics.MustRegister(signaled)
	tokens := github.NewTokens()
	ingest := &github.Ingest{Pool: d.Pool, Secrets: sec, Temporal: d.Temporal, GitHub: gh, Tokens: tokens, Log: d.Log, Signaled: signaled}
	f := &Features{Settings: st, Secrets: sec, Auth: a, GitHubClient: gh, RateLimits: limits, Tokens: tokens, Ingest: ingest,
		Models: models.NewService(d.Pool, sec, hc, d.Redactor.Redact, d.Log), ProxyMetrics: models.NewProxyMetrics(d.Metrics),
		MCP: mcpproxy.NewService(d.Pool, sec, hc, d.Log)}
	f.MCPProxy = f.MCP.NewProxy(d.Redactor.Redact)
	if d.Config.Ingress == "relay" {
		f.Relay = &github.RelayClient{URL: d.Config.RelayURL, Token: d.Config.RelayToken, HTTP: hc, Ingest: ingest, Log: d.Log.With("component", "relay-client")}
	}
	return f, nil
}

func derive(key []byte, purpose string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(purpose))
	return m.Sum(nil)
}

// probeHandler serves only the health, readiness and metrics routes, for
// the worker role.
func probeHandler(d *Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(httpMetrics(d.Metrics))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/readyz", d.readyz)
	r.Handle("/metrics", promhttp.HandlerFor(d.Metrics, promhttp.HandlerOpts{}))
	return r
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
	r.Get("/setup/github/callback", f.Auth.ManifestCallback)
	r.Post("/webhooks/github", f.Ingest.ServeHTTP)
	r.Handle("/llm/{provider}/*", f.Models.Proxy(f.ProxyMetrics))
	ra := &runner.API{Pool: d.Pool, Stores: &storage.Registry{Pool: d.Pool}, Enrich: enrichSpec(d, f), Redact: d.Redactor.Redact}
	r.Mount("/api/runner/v1", ra.Handler())
	r.Handle("/mcp/{name}", f.MCPProxy)
	r.Post("/auth/logout", f.Auth.Logout)
	r.Handle("/temporal", temporalUI(d.Config.TemporalUIURL))
	r.Handle("/temporal/*", temporalUI(d.Config.TemporalUIURL))

	api := NewAPI(r)
	api.UseMiddleware(auth.Enforce(api, func(ctx context.Context) (bool, error) { return github.Exists(ctx, d.Pool) }))
	registerOperations(api, d, f)
	r.Handle("/*", uiHandler(d))
	return r
}

func registerOperations(api huma.API, d *Deps, f *Features) {
	v1 := huma.NewGroup(api, "/api/v1")
	f.Auth.Register(v1)
	f.Settings.Register(v1)
	audit.Register(v1, d.Pool, auth.Op(auth.Admin))
	actor := func(ctx context.Context) audit.Actor { return auth.UserFrom(ctx).Actor() }
	repos.Register(v1, d.Pool, repos.Access{View: auth.Op(auth.Viewer), Edit: auth.Op(auth.Maintainer), Actor: actor,
		AllowedProfiles: func(ctx context.Context) ([]string, error) {
			var allowed []string
			row, err := f.Settings.Get(ctx, settings.AllowedProfiles)
			if err != nil {
				return nil, err
			}
			return allowed, json.Unmarshal(row.Value, &allowed)
		}})
	f.Models.Register(v1, models.Access{Admin: auth.Op(auth.Admin), Actor: actor})
	f.MCP.Register(v1, mcpproxy.Access{Admin: auth.Op(auth.Admin), Actor: actor})
	ingress := github.IngressInfo{Mode: "direct", Relay: f.Relay}
	if d.Config != nil {
		ingress.Mode, ingress.WebhookURL = d.Config.Ingress, d.Config.PublicWebhookURL
		if ingress.WebhookURL == "" {
			ingress.WebhookURL = d.Config.BaseURL + "/webhooks/github"
		}
	}
	github.Register(v1, d.Pool, auth.Op(auth.Admin), ingress)
	registerDiagnostics(v1, d, f)
}

// OpenAPI returns the OpenAPI document of the API as JSON.
func OpenAPI() ([]byte, error) {
	api := NewAPI(chi.NewRouter())
	registerOperations(api, &Deps{}, &Features{Models: &models.Service{}, MCP: &mcpproxy.Service{}})
	return json.MarshalIndent(api.OpenAPI(), "", "  ")
}

// NewAPI creates the Huma API on a router. `sayso openapi` uses it to
// export the document.
func NewAPI(r chi.Router) huma.API {
	huma.DefaultArrayNullable = false
	cfg := huma.DefaultConfig("Sayso API", "1.0.0")
	cfg.OpenAPIPath = "/api/v1/openapi"
	cfg.DocsPath = ""
	cfg.SchemasPath = "/api/v1/schemas"
	cfg.CreateHooks = nil
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
