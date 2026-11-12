//go:build e2e

package runner_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alternayte/sayso/internal/app/apptest"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	fakemcp "github.com/alternayte/sayso/internal/fakes/mcp"
	fakemodel "github.com/alternayte/sayso/internal/fakes/model"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/skills"
)

// The agent image of `just images`.
const agentImage = "sayso-agent:dev"

// containerHost is how an agent container reaches this test process.
const containerHost = "host.docker.internal"

// harness runs the API in this process and the real agent image through the
// process runner, with docker run as the agent command.
type harness struct {
	e     *apptest.Env
	model *fakemodel.Fake
	mcp   *fakemcp.Fake
	ghURL string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if err := exec.Command("docker", "image", "inspect", agentImage).Run(); err != nil {
		t.Fatalf("the agent image %s is missing: run just images", agentImage)
	}
	e := apptest.New(t, apptest.Options{ContainerHost: containerHost})
	fm := fakemodel.New()
	ms := httptest.NewServer(fm.Handler())
	t.Cleanup(ms.Close)
	fmcp := fakemcp.New()
	mcpSrv := httptest.NewServer(fmcp.Handler())
	t.Cleanup(mcpSrv.Close)
	t.Cleanup(e.F.MCPProxy.Close)
	e.GitHub.Install("acme/payments", "acme/web")
	e.SQL(t, `INSERT INTO providers (name, api_type, base_url, auth_style) VALUES ('fake', 'openai-completions', $1, 'bearer')`, ms.URL+"/v1")
	e.SQL(t, `INSERT INTO mcp_servers (name, url) VALUES ('catalog', $1)`, mcpSrv.URL)
	e.SQL(t, `UPDATE repositories SET mcp_servers = '{catalog}'`)
	return &harness{e: e, model: fm, mcp: fmcp, ghURL: e.ContainerURL(containerHost, e.GHServer)}
}

// runOpts shape one agent run.
type runOpts struct {
	repo       string
	pull       fakegh.PullOptions
	script     string
	execution  bool
	threshold  string
	overlay    runner.Overlay
	models     []runner.Model
	skill      *runner.Skill
	mcp        []string
	maxFind    int
	timeout    time.Duration
	proxy      string
	beforeHead func()
}

type outcome struct {
	run    runner.AgentRun
	reason string
	logs   string
	pull   *fakegh.Pull
}

func (h *harness) start(t *testing.T, o runOpts) outcome {
	t.Helper()
	ctx := context.Background()
	if o.repo == "" {
		o.repo = "acme/payments"
	}
	if o.threshold == "" {
		o.threshold = "minor"
	}
	if o.overlay.Toolchain == "" {
		o.overlay = runner.Overlay{Block: true, Toolchain: "none", Restore: "none"}
	}
	if o.models == nil {
		o.models = []runner.Model{{Profile: "review", Provider: "fake", Model: "fake-large"}, {Profile: "challenge", Provider: "fake", Model: "fake-small"}}
	}
	if o.maxFind == 0 {
		o.maxFind = 25
	}
	if o.timeout == 0 {
		o.timeout = 5 * time.Minute
	}
	if o.pull.Author == "" {
		o.pull.Author = "dev-alice"
	}
	if o.beforeHead != nil {
		o.beforeHead()
	}
	pr := h.e.GitHub.OpenPull(o.repo, o.pull)
	skill := o.skill
	if skill == nil {
		files, err := skills.Builtin()
		if err != nil {
			t.Fatal(err)
		}
		skill = &runner.Skill{Source: "builtin", Name: skills.BuiltinName, Files: files}
	}
	var repoID uuid.UUID
	if err := h.e.Deps.Pool.QueryRow(ctx, `SELECT id FROM repositories WHERE full_name = $1`, o.repo).Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	var mcp []runner.SpecMCP
	for _, m := range o.mcp {
		mcp = append(mcp, runner.SpecMCP{Name: m})
	}
	spec := runner.Spec{
		Stages:      []string{"review", "challenge"},
		Repository:  runner.SpecRepository{FullName: o.repo, CloneURL: h.ghURL + "/" + o.repo + ".git", DefaultBranch: "main"},
		BaseSHA:     pr.BaseSHA,
		HeadSHA:     pr.HeadSHA,
		PullRequest: runner.SpecPullRequest{Number: pr.Number, Title: pr.Title, Body: o.pull.Body, Labels: []string{}, Author: pr.Author, ChangedFiles: []string{}},
		Ledger:      []runner.LedgerEntry{},
		Overlay:     o.overlay,
		Skill:       *skill,
		Profiles:    map[string]string{"review": "review", "challenge": "challenge"},
		MCP:         mcp,
		Extensions:  []runner.SpecExtension{},
		Limits:      runner.Limits{MaxFindings: o.maxFind, MaxToolCallsPerStage: 200, TimeoutSeconds: int(o.timeout.Seconds()), SetupTimeoutSeconds: 300, ChallengeMinSeverity: o.threshold},
		Prompts:     runner.Prompts{Repository: runner.PromptLayer{Version: "1", Text: "Use the script " + o.script + "."}},
		Execution:   o.execution,
	}
	run, tok, err := runner.Create(ctx, h.e.Deps.Pool, runner.NewRun{Kind: "round", RepositoryID: &repoID, Models: o.models, Spec: spec}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	logs, err := os.CreateTemp(t.TempDir(), "agent-*.log")
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"PATH": os.Getenv("PATH")}
	cmd := []string{"docker", "run", "--rm", "--init", "--add-host=" + containerHost + ":host-gateway",
		"-e", "SAYSO_API_URL", "-e", "SAYSO_RUN_TOKEN", "-e", "SAYSO_RUN_ID"}
	if o.proxy != "" {
		vars["HTTPS_PROXY"], vars["NO_PROXY"] = o.proxy, containerHost
		cmd = append(cmd, "-e", "HTTPS_PROXY", "-e", "NO_PROXY")
	}
	cmd = append(cmd, agentImage, "agent")
	p := &runner.Process{Command: []string{"/bin/sh", "-c", strings.Join(quote(cmd), " ") + " >" + logs.Name() + " 2>&1"}, Getenv: func(k string) string { return vars[k] }}
	hd, err := p.Start(ctx, runner.Job{RunID: run.ID.String(), Token: tok, APIURL: h.e.Deps.Config.RunnerAPIURL, Timeout: o.timeout})
	if err != nil {
		t.Fatal(err)
	}
	reason, err := hd.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(logs)
	got, err := runner.New(h.e.Deps.Pool).GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if t.Failed() {
		t.Logf("agent logs:\n%s", b)
	}
	logsText := string(b)
	for _, e := range h.events(t, run.ID) {
		if e.Type == "error" || e.Status == "failed" || e.Status == "error" {
			logsText += fmt.Sprintf("\nevent %s %s %s: %s", e.Type, e.Name, e.Status, e.Preview)
		}
	}
	return outcome{run: got, reason: reason, logs: logsText, pull: pr}
}

func quote(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return out
}

// result reads result.json of a run.
func (h *harness) result(t *testing.T, run uuid.UUID) runner.Result {
	t.Helper()
	var data []byte
	err := h.e.Deps.Pool.QueryRow(context.Background(), `SELECT b.data FROM artifacts a JOIN blobs b ON b.key = a.key WHERE a.run_id = $1 AND a.name = 'result.json'`, run).Scan(&data)
	if err != nil {
		t.Fatalf("result.json: %v", err)
	}
	var r runner.Result
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

type event struct{ Stage, Type, Name, Status, Preview string }

func (h *harness) events(t *testing.T, run uuid.UUID) []event {
	t.Helper()
	rows, err := h.e.Deps.Pool.Query(context.Background(), `SELECT stage, type, name, status, preview FROM run_events WHERE run_id = $1 ORDER BY seq`, run)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []event
	for rows.Next() {
		var e event
		_ = rows.Scan(&e.Stage, &e.Type, &e.Name, &e.Status, &e.Preview)
		out = append(out, e)
	}
	return out
}

// requests returns the model requests whose system prompt holds the stage marker.
func (h *harness) requests(stage string) []map[string]any {
	var out []map[string]any
	for _, r := range h.model.Requests() {
		if !strings.Contains(r.Path, "chat/completions") || !strings.Contains(r.Body, "## Stage: "+stage) {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(r.Body), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func toolNames(req map[string]any) []string {
	var out []string
	tools, _ := req["tools"].([]any)
	for _, t := range tools {
		if f, ok := t.(map[string]any)["function"].(map[string]any); ok {
			out = append(out, fmt.Sprint(f["name"]))
		}
	}
	return out
}

func mustStatus(t *testing.T, o outcome, status string) {
	t.Helper()
	if o.run.Status != status {
		t.Fatalf("run status %s (%s), want %s; logs:\n%s", o.run.Status, o.run.ExitReason, status, o.logs)
	}
}

var _ = http.StatusOK

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
