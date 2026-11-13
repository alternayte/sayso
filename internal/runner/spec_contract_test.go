//go:build contract

package runner_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/audit"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/testenv"
)

func TestSCN_GH_008_SpecTokenIsNarrowed(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	ctx := context.Background()
	e.GitHub.Install("acme/payments", "acme/web")
	// Credentials that must never reach a run (SI-01).
	providerKey, mcpSecret := "sk-provider-secret-0001", "mcp-secret-0002"
	if _, err := e.F.Secrets.Put(ctx, audit.System, "provider.main.key", providerKey); err != nil {
		t.Fatal(err)
	}
	if _, err := e.F.Secrets.Put(ctx, audit.System, "mcp.catalog.secret", mcpSecret); err != nil {
		t.Fatal(err)
	}
	e.SQL(t, `INSERT INTO providers (name, api_type, base_url, auth_style, secret_id) SELECT 'main', 'openai-completions', 'http://model.acme.test/v1', 'bearer', id FROM secrets WHERE name = 'provider.main.key'`)
	e.SQL(t, `INSERT INTO mcp_servers (name, url, secret_header, secret_id) SELECT 'catalog', 'http://catalog.acme.test', 'X-Key', id FROM secrets WHERE name = 'mcp.catalog.secret'`)
	var repoID uuid.UUID
	if err := e.Deps.Pool.QueryRow(ctx, `SELECT id FROM repositories WHERE full_name = 'acme/payments'`).Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	pr := e.GitHub.OpenPull("acme/payments", fakeOpts())
	spec := runner.Spec{Repository: runner.SpecRepository{FullName: "acme/payments"}, BaseSHA: pr.BaseSHA, HeadSHA: pr.HeadSHA, MCP: []runner.SpecMCP{{Name: "catalog"}}}
	_, tok, err := runner.Create(ctx, e.Deps.Pool, runner.NewRun{Kind: "round", RepositoryID: &repoID, Models: []runner.Model{{Profile: "review", Provider: "main", Model: "m"}}, Spec: spec}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	code, body := call(t, e, http.MethodGet, "/api/runner/v1/spec", tok, nil)
	if code != 200 {
		t.Fatalf("spec: %d %s", code, body)
	}
	var got runner.Spec
	_ = json.Unmarshal(body, &got)
	if got.Repository.Token == "" || !strings.HasSuffix(got.Repository.CloneURL, "/acme/payments.git") {
		t.Fatalf("spec repository %+v", got.Repository)
	}
	grants := e.GitHub.Grants()
	g := grants[len(grants)-1]
	if strings.Join(g.Repositories, ",") != "payments" || len(g.Permissions) != 3 || g.Permissions["contents"] != "read" ||
		g.Permissions["pull_requests"] != "read" || g.Permissions["metadata"] != "read" {
		t.Fatalf("spec token grant %+v", g)
	}
	if got.Models[0].BaseURL != e.Deps.Config.RunnerAPIURL+"/llm/main" || got.MCP[0].URL != e.Deps.Config.RunnerAPIURL+"/mcp/catalog" {
		t.Fatalf("proxy entries %+v %+v", got.Models, got.MCP)
	}
	for _, secret := range []string{providerKey, mcpSecret, testenv.SecretKey(), "acme-client-secret", apptest.WebhookSecret} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("the spec holds a credential: %q", secret)
		}
	}
	// A second spec request mints a new token; tokens are never stored.
	call(t, e, http.MethodGet, "/api/runner/v1/spec", tok, nil)
	if n := len(e.GitHub.Grants()); n != len(grants)+1 {
		t.Fatalf("grants after the second spec request: %d", n)
	}
	if n := e.Count(t, `SELECT count(*) FROM agent_runs WHERE spec::text LIKE '%ghs_%'`); n != 0 {
		t.Fatal("a GitHub token is stored in the run spec")
	}
}

func fakeOpts() fakegh.PullOptions { return fakegh.PullOptions{Author: "dev-alice"} }
