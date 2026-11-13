//go:build contract

package mcpproxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alternayte/sayso/internal/app/apptest"
	fakemcp "github.com/alternayte/sayso/internal/fakes/mcp"
	"github.com/alternayte/sayso/internal/mcpproxy"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/secrets"
)

type env struct {
	*apptest.Env
	admin *http.Client
	fake  *fakemcp.Fake
	url   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := apptest.New(t, apptest.Options{BootstrapAdmins: []string{"octo-admin"}})
	admin, _ := e.SignIn(t, "octo-admin")
	f := fakemcp.New()
	srv := httptest.NewServer(f.Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(e.F.MCPProxy.Close)
	return &env{Env: e, admin: admin, fake: f, url: srv.URL}
}

func (e *env) server(t *testing.T, in mcpproxy.ServerInput) {
	t.Helper()
	if in.URL == "" {
		in.URL = e.url
	}
	if code := apptest.JSON(t, e.admin, http.MethodPost, e.Base+"/api/v1/mcp-servers", in, nil); code != http.StatusCreated {
		t.Fatalf("create %s: %d", in.Name, code)
	}
}

func (e *env) test(t *testing.T, name string) map[string]mcpproxy.ToolInfo {
	t.Helper()
	var tools []mcpproxy.ToolInfo
	if code := apptest.JSON(t, e.admin, http.MethodPost, e.Base+"/api/v1/mcp-servers/"+name+"/test", nil, &tools); code != 200 {
		t.Fatalf("test %s: %d", name, code)
	}
	out := map[string]mcpproxy.ToolInfo{}
	for _, x := range tools {
		out[x.Name] = x
	}
	return out
}

func TestSCN_MCP_001_TestListsToolsWithAnnotations(t *testing.T) {
	e := newEnv(t)
	e.server(t, mcpproxy.ServerInput{Name: "catalog", Headers: map[string]string{"X-Team": "review"}, SecretHeader: "X-Catalog-Key", Secret: &secrets.Input{Value: "cat-key-0001"}})
	tools := e.test(t, "catalog")
	if len(tools) != 3 || !tools["catalog_search"].ReadOnly || tools["ticket_create"].ReadOnly || tools["ticket_create"].Destructive == nil || !*tools["ticket_create"].Destructive {
		t.Fatalf("tools %+v", tools)
	}
	h := e.fake.Headers()
	if len(h) == 0 || h[0].Get("X-Catalog-Key") != "cat-key-0001" || h[0].Get("X-Team") != "review" {
		t.Fatalf("headers at the server: %v", h)
	}
	// The stored list shows on the server page.
	var list []mcpproxy.ServerBody
	apptest.JSON(t, e.admin, http.MethodGet, e.Base+"/api/v1/mcp-servers", nil, &list)
	if len(list) != 1 || len(list[0].Tools) != 3 || list[0].Secret.Last4 != "0001" {
		t.Fatalf("server list %+v", list)
	}
}

func TestSCN_MCP_002_ReadOnlyByDefault(t *testing.T) {
	e := newEnv(t)
	e.server(t, mcpproxy.ServerInput{Name: "plain"})
	tools := e.test(t, "plain")
	if !tools["catalog_search"].Allowed || !tools["catalog_owner"].Allowed || tools["ticket_create"].Allowed {
		t.Fatalf("default allowlist %+v", tools)
	}
	// A pattern without allow_write is refused.
	if code := apptest.JSON(t, e.admin, http.MethodPost, e.Base+"/api/v1/mcp-servers", mcpproxy.ServerInput{Name: "bad", URL: e.url, AllowTools: []string{"ticket_*"}}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("pattern without allow_write: %d", code)
	}
	// allow_write without a pattern still blocks the write tool.
	e.server(t, mcpproxy.ServerInput{Name: "write-no-pattern", AllowWrite: true})
	if e.test(t, "write-no-pattern")["ticket_create"].Allowed {
		t.Fatal("allow_write alone allowed the write tool")
	}
	e.server(t, mcpproxy.ServerInput{Name: "write", AllowWrite: true, AllowTools: []string{"ticket_*"}})
	if !e.test(t, "write")["ticket_create"].Allowed {
		t.Fatal("allow_write and a pattern did not allow the write tool")
	}
}

func TestSCN_MCP_003_ProxyFiltersToolsAndLogsCalls(t *testing.T) {
	e := newEnv(t)
	e.server(t, mcpproxy.ServerInput{Name: "catalog", SecretHeader: "X-Catalog-Key", Secret: &secrets.Input{Value: "cat-key-0001"}})
	e.server(t, mcpproxy.ServerInput{Name: "other"})
	e.GitHub.Install("acme/payments", "acme/web")
	e.SQL(t, `UPDATE repositories SET mcp_servers = '{catalog}' WHERE full_name = 'acme/payments'`)
	repo := func(name string) uuid.UUID {
		var id uuid.UUID
		if err := e.Deps.Pool.QueryRow(context.Background(), `SELECT id FROM repositories WHERE full_name = $1`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	newRun := func(repoName string, servers ...string) string {
		var ms []runner.SpecMCP
		for _, s := range servers {
			ms = append(ms, runner.SpecMCP{Name: s})
		}
		u := repo(repoName)
		_, tok, err := runner.Create(context.Background(), e.Deps.Pool, runner.NewRun{Kind: "round", RepositoryID: &u, Spec: runner.Spec{MCP: ms}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	tok := newRun("acme/payments", "catalog")
	ctx := context.Background()
	cs := connect(t, e.Base+"/mcp/catalog", tok)
	defer func() { _ = cs.Close() }()
	var names []string
	for tl, err := range cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	if strings.Join(names, ",") != "catalog_owner,catalog_search" {
		t.Fatalf("tools through the proxy: %v", names)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "catalog_search", Arguments: map[string]any{"query": "ledger"}})
	if err != nil || res.IsError {
		t.Fatalf("allowed call: %v %+v", err, res)
	}
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ticket_create", Arguments: map[string]any{"title": "x"}}); err == nil {
		t.Fatal("the blocked tool was called")
	}
	if calls := e.fake.Calls(); strings.Join(calls, ",") != "catalog_search" {
		t.Fatalf("calls at the server: %v", calls)
	}
	for _, h := range e.fake.Headers() {
		if h.Get("X-Catalog-Key") != "cat-key-0001" || strings.Contains(h.Get("Authorization"), tok) {
			t.Fatalf("upstream headers %v", h)
		}
	}
	if n := e.Count(t, `SELECT count(*) FROM run_events WHERE type = 'mcp_call' AND name = 'catalog/catalog_search' AND status = 'ok' AND preview LIKE '%ledger%'`); n != 1 {
		t.Fatalf("mcp_call events: %d", n)
	}
	// A server the run does not have, and a run of a repository that does not allow it, get 403.
	if code := status(t, e.Base+"/mcp/other", tok); code != http.StatusForbidden {
		t.Fatalf("server not in the run: %d", code)
	}
	web := newRun("acme/web", "catalog")
	if code := status(t, e.Base+"/mcp/catalog", web); code != http.StatusForbidden {
		t.Fatalf("repository without the server: %d", code)
	}
	if code := status(t, e.Base+"/mcp/catalog", "srt_unknown"); code != http.StatusUnauthorized {
		t.Fatalf("unknown token: %d", code)
	}
}

type bearer struct {
	tok  string
	base http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return b.base.RoundTrip(r)
}

func connect(t *testing.T, url, tok string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: bearer{tok, http.DefaultTransport}}, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func status(t *testing.T, url, tok string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}
