// Package batchfix pins the batch fix spec: one fix run for several
// findings, one commit for each fix that passes the check.
package batchfix

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
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

const ledgerText = `package ledger

// Post adds amount cents to the balance.
func Post(balance, amount int64) int64 { return balance + amount }

// Sub takes amount cents from the balance.
func Sub(balance, amount int64) int64 { return balance - amount }
`

func finding(sev, title string, line int) runner.Finding {
	return runner.Finding{CandidateID: title, Severity: sev, Category: "correctness", Title: title, Body: title + ".",
		Path: ledgerFile, StartLine: line, EndLine: line, Evidence: runner.Evidence{Kind: "reasoning", Detail: "No check."}}
}

func patch(id, line, old, new string) runner.Patch {
	return runner.Patch{FindingID: id, Title: "Fix " + id, Description: "Adds a check.",
		Diff: "--- a/" + ledgerFile + "\n+++ b/" + ledgerFile + "\n@@ -" + line + " +" + line + " @@\n-" + old + "\n+" + new + "\n"}
}

var (
	postOld = "func Post(balance, amount int64) int64 { return balance + amount }"
	postNew = "func Post(balance, amount int64) int64 { return balance + max(amount, 0) }"
	subOld  = "func Sub(balance, amount int64) int64 { return balance - amount }"
	subNew  = "func Sub(balance, amount int64) int64 { return balance - max(amount, 0) }"
)

// flow opens a pull request whose round reports S-1 major, S-2 nit and S-3
// blocker. fix answers the fix run and records the spec it got.
func flow(t *testing.T, fix func(runner.Spec) runner.Result, extra func(e *apptest.Env)) (*apptest.Env, *fakegh.Pull) {
	t.Helper()
	ctx := context.Background()
	e := apptest.New(t, apptest.Options{Temporal: true})
	if _, code := e.GitHub.Install("acme/payments"); code != http.StatusOK {
		t.Fatalf("install: %d", code)
	}
	e.SQL(t, `UPDATE repositories SET mode = 'comment'`)
	e.SQL(t, `INSERT INTO providers (name, api_type, base_url, auth_style) VALUES ('fake', 'openai-completions', 'http://127.0.0.1:1/v1', 'bearer')`)
	e.SQL(t, `UPDATE profiles SET targets = '[{"provider":"fake","model":"fake-large","price_in":0,"price_out":0,"price_cache_read":0}]'`)
	if _, err := e.F.Settings.Set(ctx, audit.System, "debounce_seconds", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if extra != nil {
		extra(e)
	}
	e.Deps.Runner = &runnertest.Scripted{Script: func(s runner.Spec) runnertest.Answer {
		if s.Kind == "fix" {
			r := fix(s)
			return runnertest.Answer{Result: &r}
		}
		return runnertest.Answer{Result: &runner.Result{
			Assessment: &runner.Assessment{TLDR: "Small change.", Risk: "low", RiskReasons: []string{"small"}, WhereToLook: []runner.WhereToLook{}, Impact: []runner.Impact{}},
			Findings: []runner.Finding{finding("major", "Negative amounts pass Post", 4), finding("nit", "Comment wording", 3),
				finding("blocker", "Sub can raise the balance", 7)},
		}}
	}}
	stop, err := app.StartWorker(ctx, e.Deps, e.F)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	pr := e.GitHub.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice", Files: map[string]string{ledgerFile: ledgerText}})
	if _, code := e.GitHub.PullEvent("acme/payments", pr.Number, "opened", nil); code != http.StatusOK {
		t.Fatalf("opened: %d", code)
	}
	waitFor(t, 2*time.Minute, "the round", func() bool {
		return e.Count(t, `SELECT count(*) FROM rounds WHERE status = 'done'`) == 1
	})
	return e, pr
}

func fixPull(t *testing.T, e *apptest.Env) fakegh.Pull {
	t.Helper()
	var out fakegh.Pull
	waitFor(t, 2*time.Minute, "the fix pull request", func() bool {
		for _, p := range e.GitHub.Pulls("acme/payments") {
			if strings.HasPrefix(p.HeadRef, "sayso/fix/") {
				out = p
				return true
			}
		}
		return false
	})
	return out
}

// SCN-CMD-007: /sayso fix all takes the blocker and the major finding, and
// one pull request has one commit for each.
func TestSCN_CMD_007_FixAllOpensOnePullRequest(t *testing.T) {
	var mu sync.Mutex
	var got []string
	e, pr := flow(t, func(s runner.Spec) runner.Result {
		mu.Lock()
		for _, f := range s.Fix.Findings {
			got = append(got, f.Finding.ID)
		}
		mu.Unlock()
		return runner.Result{Patches: []runner.Patch{patch("S-3", "7", subOld, subNew), patch("S-1", "4", postOld, postNew)},
			FixResults: []runner.FixResult{{FindingID: "S-3", Status: "fixed"}, {FindingID: "S-1", Status: "fixed"}}}
	}, nil)
	if _, code := e.GitHub.AddIssueComment("acme/payments", pr.Number, "dev-alice", "/sayso fix all"); code != http.StatusOK {
		t.Fatalf("comment: %d", code)
	}
	fix := fixPull(t, e)
	mu.Lock()
	if !slices.Equal(got, []string{"S-3", "S-1"}) {
		t.Fatalf("fix run findings %v, want S-3 then S-1", got)
	}
	mu.Unlock()
	if !strings.Contains(fix.HeadRef, "-batch-") {
		t.Fatalf("branch %s", fix.HeadRef)
	}
	subjects, err := e.GitHub.Subjects("acme/payments", pr.HeadSHA, fix.HeadRef)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(subjects, []string{"Fix S-3: Fix S-3", "Fix S-1: Fix S-1"}) {
		t.Fatalf("commits %q", subjects)
	}
	text, err := e.GitHub.FileAt("acme/payments", ledgerFile, fix.HeadRef)
	if err != nil || !strings.Contains(text, postNew) || !strings.Contains(text, subNew) {
		t.Fatalf("the branch lacks a fix: %v\n%s", err, text)
	}
	if n := len(e.GitHub.Pulls("acme/payments")); n != 2 {
		t.Fatalf("%d pull requests, want the original and one fix", n)
	}
}

// SCN-CMD-008: a fix that fix_check drops is not committed, and the pull
// request body says why.
func TestSCN_CMD_008_DroppedFixIsNotCommitted(t *testing.T) {
	e, pr := flow(t, func(s runner.Spec) runner.Result {
		return runner.Result{Patches: []runner.Patch{patch("S-1", "4", postOld, postNew)},
			FixResults: []runner.FixResult{{FindingID: "S-3", Status: "dropped", Reason: "the patch does not resolve the finding"}, {FindingID: "S-1", Status: "fixed"}}}
	}, nil)
	if _, code := e.GitHub.AddIssueComment("acme/payments", pr.Number, "dev-alice", "/sayso fix S-3 S-1"); code != http.StatusOK {
		t.Fatalf("comment: %d", code)
	}
	fix := fixPull(t, e)
	subjects, err := e.GitHub.Subjects("acme/payments", pr.HeadSHA, fix.HeadRef)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(subjects, []string{"Fix S-1: Fix S-1"}) {
		t.Fatalf("commits %q", subjects)
	}
	if !strings.Contains(fix.Body, "S-3: dropped by the check: the patch does not resolve the finding") {
		t.Fatalf("body:\n%s", fix.Body)
	}
}

// SCN-CMD-009: a batch above max_fix_batch is refused with the count.
func TestSCN_CMD_009_BatchAboveTheLimitIsRefused(t *testing.T) {
	e, pr := flow(t, func(s runner.Spec) runner.Result {
		t.Error("the fix run started")
		return runner.Result{}
	}, func(e *apptest.Env) {
		if _, err := e.F.Settings.Set(context.Background(), audit.System, "max_fix_batch", []byte("2")); err != nil {
			t.Fatal(err)
		}
	})
	if _, code := e.GitHub.AddIssueComment("acme/payments", pr.Number, "dev-alice", "/sayso fix S-1 S-2 S-3"); code != http.StatusOK {
		t.Fatalf("comment: %d", code)
	}
	waitFor(t, time.Minute, "the refusal", func() bool {
		for _, c := range e.GitHub.IssueComments("acme/payments", pr.Number) {
			if strings.Contains(c.Body, "3 findings; the limit is 2") {
				return true
			}
		}
		return false
	})
}
