// Package failearly pins the fail early and visible spec: a round without
// a model target fails before its agent run, the findings of a finished
// stage stay visible, and a failed command shows on GitHub.
package failearly

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/audit"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/runner/runnertest"
)

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

const ledgerFile = "ledger/ledger.go"

func start(t *testing.T, mode string, script func(runner.Spec) runnertest.Answer, extra func(e *apptest.Env)) (*apptest.Env, *fakegh.Pull) {
	t.Helper()
	ctx := context.Background()
	e := apptest.New(t, apptest.Options{Temporal: true})
	if _, code := e.GitHub.Install("acme/payments"); code != http.StatusOK {
		t.Fatalf("install: %d", code)
	}
	e.SQL(t, `UPDATE repositories SET mode = $1`, mode)
	e.SQL(t, `INSERT INTO providers (name, api_type, base_url, auth_style) VALUES ('fake', 'openai-completions', 'http://127.0.0.1:1/v1', 'bearer')`)
	e.SQL(t, `UPDATE profiles SET targets = '[{"provider":"fake","model":"fake-large","price_in":0,"price_out":0,"price_cache_read":0}]'`)
	if _, err := e.F.Settings.Set(ctx, audit.System, "debounce_seconds", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if extra != nil {
		extra(e)
	}
	e.Deps.Runner = &runnertest.Scripted{Script: script}
	stop, err := app.StartWorker(ctx, e.Deps, e.F)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	pr := e.GitHub.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice", Files: map[string]string{ledgerFile: "package ledger\n\nfunc Post(b, a int64) int64 { return b + a }\n"}})
	if _, code := e.GitHub.PullEvent("acme/payments", pr.Number, "opened", nil); code != http.StatusOK {
		t.Fatalf("opened: %d", code)
	}
	return e, pr
}

func done(spec runner.Spec) runnertest.Answer {
	return runnertest.Answer{Result: &runner.Result{
		Assessment: &runner.Assessment{TLDR: "Small change.", Risk: "low", RiskReasons: []string{"small"}, WhereToLook: []runner.WhereToLook{}, Impact: []runner.Impact{}}}}
}

// SCN-LIFE-014: a stage without a model target fails the round before its
// agent run starts.
func TestSCN_LIFE_014_NoModelTargetFailsFirst(t *testing.T) {
	e, _ := start(t, "shadow", done, func(e *apptest.Env) {
		e.SQL(t, `UPDATE profiles SET targets = '[]' WHERE name = 'challenge'`)
	})
	waitFor(t, 2*time.Minute, "a failed round", func() bool {
		return e.Count(t, `SELECT count(*) FROM rounds WHERE status = 'failed'`) == 1
	})
	var reason string
	if err := e.Deps.Pool.QueryRow(context.Background(), `SELECT failure_reason FROM rounds`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "no_model_target: stage challenge, profile challenge" {
		t.Fatalf("reason %q", reason)
	}
	if n := e.Count(t, `SELECT count(*) FROM agent_runs`); n != 0 {
		t.Fatalf("%d agent runs", n)
	}
}

// SCN-LIFE-015: when the challenge stage fails, the review findings show in
// the preview as not challenged and stay out of the ledger.
func TestSCN_LIFE_015_UnchallengedFindingsInPreview(t *testing.T) {
	e, _ := start(t, "shadow", func(spec runner.Spec) runnertest.Answer {
		a := done(spec)
		a.ExitStatus, a.Reason = "incomplete_output", "challenge_result is missing for c1"
		a.Result.Findings = []runner.Finding{{CandidateID: "c1", Severity: "major", Category: "correctness", Title: "Negative amounts pass", Body: "b",
			Path: ledgerFile, StartLine: 3, EndLine: 3, Evidence: runner.Evidence{Kind: "reasoning", Detail: "d"}}}
		return a
	}, nil)
	waitFor(t, 2*time.Minute, "a failed round", func() bool {
		return e.Count(t, `SELECT count(*) FROM rounds WHERE status = 'failed' AND preview IS NOT NULL`) == 1
	})
	var reason string
	var raw []byte
	if err := e.Deps.Pool.QueryRow(context.Background(), `SELECT failure_reason, preview FROM rounds`).Scan(&reason, &raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "challenge_result is missing for c1") {
		t.Fatalf("reason %q", reason)
	}
	var pv struct {
		Unchallenged []runner.Finding `json:"unchallenged"`
	}
	_ = json.Unmarshal(raw, &pv)
	if len(pv.Unchallenged) != 1 || pv.Unchallenged[0].Title != "Negative amounts pass" {
		t.Fatalf("preview %s", raw)
	}
	if n := e.Count(t, `SELECT count(*) FROM findings`); n != 0 {
		t.Fatalf("%d findings in the ledger", n)
	}
}

// SCN-CMD-006: a command whose activity fails after its retries gets the
// eyes and confused reactions and a failed_commands row.
func TestSCN_CMD_006_FailedCommandIsVisible(t *testing.T) {
	e, pr := start(t, "comment", done, nil)
	waitFor(t, 2*time.Minute, "the round", func() bool {
		return e.Count(t, `SELECT count(*) FROM rounds WHERE status = 'done'`) == 1
	})
	e.GitHub.FailCommentReads(true)
	c, code := e.GitHub.AddIssueComment("acme/payments", pr.Number, "dev-alice", "/sayso review")
	if code != http.StatusOK {
		t.Fatalf("comment: %d", code)
	}
	waitFor(t, 3*time.Minute, "the failed command", func() bool {
		return e.Count(t, `SELECT count(*) FROM failed_commands`) == 1
	})
	got := map[string]bool{}
	for _, r := range e.GitHub.CommentReactions() {
		if r.CommentID == c {
			got[r.Content] = true
		}
	}
	if !got["eyes"] || !got["confused"] {
		t.Fatalf("reactions %v", got)
	}
	replied := false
	for _, ic := range e.GitHub.IssueComments("acme/payments", pr.Number) {
		replied = replied || strings.HasPrefix(ic.Body, "Sayso could not run this command:")
	}
	if !replied {
		t.Fatal("no reply with the reason")
	}
}
