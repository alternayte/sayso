// Package apptest runs the API handler in-process against a real database
// and the fake GitHub, for contract and integration tests.
package apptest

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.temporal.io/sdk/client"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/audit"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/settings"
	"github.com/alternayte/sayso/internal/testenv"
)

// Clock is a settable test clock.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Options change the environment.
type Options struct {
	SetupToken      string
	BootstrapAdmins []string
	NoApp           bool
}

// Env is one running API with its fakes.
type Env struct {
	Deps   *app.Deps
	F      *app.Features
	GitHub *fakegh.Fake
	Server *httptest.Server
	Base   string
	Clock  *Clock
}

// New starts the API handler on a fresh database. Unless NoApp is set, the
// GitHub App for the fake acme organization exists.
func New(t *testing.T, o Options) *Env {
	t.Helper()
	ctx := context.Background()
	pool := testenv.DB(t)
	fake := fakegh.New()
	ghSrv := httptest.NewServer(fake.Handler())
	t.Cleanup(ghSrv.Close)

	srv := httptest.NewUnstartedServer(nil)
	base := "http://" + srv.Listener.Addr().String()
	cfg, _, err := app.LoadConfig(func(k string) string {
		return map[string]string{
			"SAYSO_DATABASE_URL": "postgres://in-process", "SAYSO_SECRET_KEY": testenv.SecretKey(),
			"SAYSO_BASE_URL": base, "SAYSO_SETUP_TOKEN": o.SetupToken,
			"SAYSO_BOOTSTRAP_ADMINS": strings.Join(o.BootstrapAdmins, ","),
		}[k]
	}, nil, app.Need{Database: true, SecretKey: true})
	if err != nil {
		t.Fatal(err)
	}
	tc, err := client.NewLazyClient(client.Options{HostPort: "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	red := app.NewRedactor()
	d := &app.Deps{Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Redactor: red, Pool: pool, Temporal: tc, Metrics: prometheus.NewRegistry()}
	f, err := app.NewFeatures(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	clock := &Clock{now: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)}
	f.Auth.Now = clock.Now
	for k, v := range map[string]string{settings.GitHubWebURL: ghSrv.URL, settings.GitHubAPIURL: ghSrv.URL} {
		raw, _ := json.Marshal(v)
		if _, err := f.Settings.Set(ctx, audit.System, k, raw); err != nil {
			t.Fatal(err)
		}
	}
	if !o.NoApp {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
		a := &github.App{AppID: 77, Slug: "sayso-acme", Org: "acme", ClientID: fake.ClientID, ClientSecret: fake.Secret, PrivateKeyPEM: pemKey}
		if err := github.SaveApp(ctx, pool, f.Secrets, a, "acme-webhook-secret", ghSrv.URL+"/apps/sayso-acme"); err != nil {
			t.Fatal(err)
		}
	}
	srv.Config.Handler = app.NewHandler(d, f)
	srv.Start()
	t.Cleanup(srv.Close)
	return &Env{Deps: d, F: f, GitHub: fake, Server: srv, Base: base, Clock: clock}
}

// Client returns an HTTP client with its own cookie jar that sends the
// Origin of the API, like the browser UI does.
func (e *Env) Client(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Transport: originTransport{base: e.Base}}
}

type originTransport struct{ base string }

func (o originTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Origin") == "" && strings.HasPrefix(r.URL.String(), o.base) {
		r = r.Clone(r.Context())
		r.Header.Set("Origin", o.base)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// SignIn runs the GitHub sign-in flow as login and returns the client and
// the final redirect location.
func (e *Env) SignIn(t *testing.T, login string) (*http.Client, string) {
	t.Helper()
	c := e.Client(t)
	var last string
	c.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		last = req.URL.String()
		if strings.HasPrefix(last, e.Base) && !strings.Contains(last, "/auth/") {
			return http.ErrUseLastResponse
		}
		return nil
	}
	e.GitHub.SignInAs(login)
	resp, err := c.Get(e.Base + "/auth/github/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	c.CheckRedirect = nil
	return c, strings.TrimPrefix(last, e.Base)
}

// JSON sends a request with an optional JSON body and decodes the reply.
func JSON(t *testing.T, c *http.Client, method, url string, in, out any) int {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}
