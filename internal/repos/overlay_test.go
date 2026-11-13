package repos_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/repos"
	"github.com/alternayte/sayso/internal/runner"
)

// overlayAt commits .sayso.yml on main of acme/payments and returns the
// effective configuration read from that base commit.
func overlayAt(t *testing.T, e *apptest.Env, file string) repos.Effective {
	t.Helper()
	ctx := context.Background()
	sha := e.GitHub.CommitMain("acme/payments", map[string]string{repos.OverlayPath: file})
	app, err := github.LoadApp(ctx, e.Deps.Pool, e.F.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	gh, err := e.F.GitHubClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := gh.InstallationToken(ctx, app, e.GitHub.InstallID, github.TokenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := gh.FileAt(ctx, tok, e.GitHub.InstallID, "acme/payments", repos.OverlayPath, sha)
	if err != nil {
		t.Fatal(err)
	}
	o, warn, err := repos.ParseOverlay(raw)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := repos.New(e.Deps.Pool).ByFullName(ctx, "acme/payments")
	if err != nil {
		t.Fatal(err)
	}
	return repos.Resolve(repo, o, warn, repos.Limits{AllowedProfiles: []string{"review", "review-strong", "challenge", "reply", "fix"}, MaxInlineComments: 10})
}

func TestSCN_RUN_015_ExecutionFromUIAndFile(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	e.GitHub.Install("acme/payments")
	cases := []struct {
		ui, file string
		want     bool
		warning  bool
	}{
		{"true", "execution: on\n", true, true},
		{"true", "execution: off\n", false, false},
		{"false", "execution: on\n", false, true},
		{"false", "", false, false},
	}
	for _, c := range cases {
		e.SQL(t, `UPDATE repositories SET execution = $1 WHERE full_name = 'acme/payments'`, c.ui == "true")
		got := overlayAt(t, e, c.file+"setup: [\"make tools\"]\n")
		warned := false
		for _, w := range got.Warnings {
			if strings.Contains(w, "execution") {
				warned = true
			}
		}
		if got.Execution != c.want || warned != c.warning {
			t.Fatalf("ui %s, file %q: execution %v warning %v (%v)", c.ui, c.file, got.Execution, warned, got.Warnings)
		}
		if !got.Execution && (got.Toolchain != "none" || got.Restore != "none" || len(got.Setup) != 0) {
			t.Fatalf("execution off still prepares the environment: %+v", got)
		}
	}
}

func TestSCN_MCP_004_OverlayNarrowsServers(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	e.GitHub.Install("acme/payments")
	e.SQL(t, `INSERT INTO mcp_servers (name, url) VALUES ('catalog', 'http://catalog.acme.test/mcp'), ('tickets', 'http://tickets.acme.test/mcp')`)
	e.SQL(t, `UPDATE repositories SET mcp_servers = '{catalog,tickets}' WHERE full_name = 'acme/payments'`)
	eff := overlayAt(t, e, "mcp: [catalog, unknown]\nmode: gate\nprofiles: {review: review-strong}\n")
	if strings.Join(eff.MCP, ",") != "catalog" || eff.Sources["mcp"] != repos.SourceFile {
		t.Fatalf("effective servers %v (%s)", eff.MCP, eff.Sources["mcp"])
	}
	// The run spec built from it has one server.
	var mcp []runner.SpecMCP
	for _, n := range eff.MCP {
		mcp = append(mcp, runner.SpecMCP{Name: n})
	}
	_, tok, err := runner.Create(context.Background(), e.Deps.Pool, runner.NewRun{Kind: "noop", Spec: runner.Spec{MCP: mcp}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, e.Base+"/api/runner/v1/spec", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var spec runner.Spec
	_ = json.Unmarshal(b, &spec)
	if len(spec.MCP) != 1 || spec.MCP[0].Name != "catalog" || !strings.HasSuffix(spec.MCP[0].URL, "/mcp/catalog") {
		t.Fatalf("spec mcp %+v", spec.MCP)
	}
	// The overlay sets an allowed profile and cannot set the mode.
	if eff.Profiles["review"] != "review-strong" || eff.Sources["profiles.review"] != repos.SourceFile {
		t.Fatalf("profiles %v", eff.Profiles)
	}
	modeWarned := false
	for _, w := range eff.Warnings {
		if strings.Contains(w, `"mode"`) {
			modeWarned = true
		}
	}
	if !modeWarned {
		t.Fatalf("no warning for mode: %v", eff.Warnings)
	}
}
