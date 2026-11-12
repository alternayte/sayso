package models_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app/apptest"
	fakemodel "github.com/alternayte/sayso/internal/fakes/model"
	"github.com/alternayte/sayso/internal/models"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/secrets"
)

// env is the API with an admin client and a fake model server.
type env struct {
	*apptest.Env
	admin *http.Client
	model *fakemodel.Fake
	url   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := apptest.New(t, apptest.Options{BootstrapAdmins: []string{"octo-admin"}})
	admin, _ := e.SignIn(t, "octo-admin")
	fm := fakemodel.New()
	srv := httptest.NewServer(fm.Handler())
	t.Cleanup(srv.Close)
	return &env{Env: e, admin: admin, model: fm, url: srv.URL}
}

// provider creates a provider on the fake model.
func (e *env) provider(t *testing.T, in models.ProviderInput) models.ProviderBody {
	t.Helper()
	if in.BaseURL == "" {
		in.BaseURL = e.url + "/v1"
	}
	var out models.ProviderBody
	if code := apptest.JSON(t, e.admin, http.MethodPost, e.Base+"/api/v1/providers", in, &out); code != http.StatusCreated {
		t.Fatalf("create provider %s: %d", in.Name, code)
	}
	return out
}

// tested creates a provider and runs its smoke test.
func (e *env) tested(t *testing.T, in models.ProviderInput) {
	t.Helper()
	e.provider(t, in)
	var sm models.Smoke
	if code := apptest.JSON(t, e.admin, http.MethodPost, e.Base+"/api/v1/providers/"+in.Name+"/test", map[string]string{}, &sm); code != http.StatusOK || !sm.OK() {
		t.Fatalf("smoke test of %s: %d %+v", in.Name, code, sm)
	}
}

// run creates a running agent run that may call the given models.
func (e *env) run(t *testing.T, budget float64, ms ...runner.Model) (runner.AgentRun, string) {
	t.Helper()
	r, tok, err := runner.Create(context.Background(), e.Deps.Pool, runner.NewRun{Kind: "noop", Models: ms, BudgetUSD: budget}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return r, tok
}

// call sends a request through the model proxy.
func (e *env) call(t *testing.T, provider, path string, header http.Header, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.Base+"/llm/"+provider+"/"+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func key(v string) *secrets.Input { return &secrets.Input{Value: v} }
