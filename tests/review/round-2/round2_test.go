// Package round2 holds the failing tests of review round 2 (§27).
package round2

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/models"
	"github.com/alternayte/sayso/internal/publish"
	"github.com/alternayte/sayso/internal/runner"
)

// Finding 3 (SI-03): the model proxy put any agent path on the provider
// base URL, with the provider key.
func TestReview2_03_ModelProxyForwardsInferenceCallsOnly(t *testing.T) {
	cases := []struct {
		api, path string
		ok        bool
	}{
		{"openai-completions", "chat/completions", true},
		{"openai-responses", "responses", true},
		{"anthropic-messages", "messages", true},
		{"google-generative-ai", "models/gemini-2.5-pro:streamGenerateContent", true},
		{"openai-completions", "%2e%2e/%2e%2e/admin/keys", false},
		{"openai-completions", "../admin/keys", false},
		{"openai-completions", "files", false},
		{"openai-completions", "fine_tuning/jobs", false},
		{"anthropic-messages", "messages/../organizations/keys", false},
		{"google-generative-ai", "models/x:generateContent/../../keys", false},
	}
	for _, c := range cases {
		cfg := models.Config{APIType: c.api, BaseURL: "https://llm.acme.test/v1"}
		_, err := cfg.Endpoint(c.path, "")
		if (err == nil) != c.ok {
			t.Errorf("%s %s: err %v", c.api, c.path, err)
		}
	}
}

// Finding 3 through the proxy: the upstream gets no request.
func TestReview2_03_ModelProxyRefusesOtherPaths(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(up.Close)
	e := apptest.New(t, apptest.Options{})
	e.SQL(t, `INSERT INTO providers (name, api_type, base_url, auth_style, models) VALUES ('acme-llm', 'openai-completions', $1, 'bearer', '{fake-large}')`, up.URL+"/v1")
	_, tok, err := runner.Create(context.Background(), e.Deps.Pool, runner.NewRun{Kind: "noop", Spec: runner.Spec{Kind: "noop"},
		Models: []runner.Model{{Profile: "review", Provider: "acme-llm", Model: "fake-large"}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/llm/acme-llm/%2e%2e/%2e%2e/admin/keys", "/llm/acme-llm/files"} {
		req, _ := http.NewRequest(http.MethodPost, e.Base+p, strings.NewReader(`{"model":"fake-large"}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("the provider got %d requests", hits.Load())
	}
}

// Finding 4 (SI-17): an encoded slash in a segment hid a dot segment from
// the feed proxy check.
func TestReview2_04_FeedProxyRefusesAnEncodedSlash(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(up.Close)
	e := apptest.New(t, apptest.Options{})
	e.SQL(t, `INSERT INTO package_feeds (name, kind, upstream_url) VALUES ('acme-npm', 'npm', $1)`, up.URL+"/npm/")
	_, tok, err := runner.Create(context.Background(), e.Deps.Pool, runner.NewRun{Kind: "noop", Spec: runner.Spec{Kind: "noop", Feeds: []runner.SpecFeed{{Name: "acme-npm"}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, e.Base+"/pkg/acme-npm/..%2F..%2Fapi%2Fsecurity", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || hits.Load() != 0 {
		t.Fatalf("status %d, upstream requests %d", resp.StatusCode, hits.Load())
	}
}

// Finding 5 (SI-16): a run stored result.json itself, past the result
// validation, and the risk text went to GitHub raw.
func TestReview2_05_RunCannotStoreItsOwnResult(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	_, tok, err := runner.Create(context.Background(), e.Deps.Pool, runner.NewRun{Kind: "noop", Spec: runner.Spec{Kind: "noop"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, e.Base+"/api/runner/v1/artifacts/result.json", strings.NewReader(`{"assessment":{"risk":"high @acme/security"}}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("PUT result.json: %d", resp.StatusCode)
	}
	c := publish.RenderCheck("gate", "done", "pass", "", &runner.Assessment{Risk: "high @acme/security"}, nil)
	if strings.Contains(c.Title, "@acme") {
		t.Fatalf("check title %q", c.Title)
	}
}
