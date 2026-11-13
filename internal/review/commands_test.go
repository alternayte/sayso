package review_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/review/signals"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/runner/runnertest"
)

// replyOrRound answers reply runs with a hold and rounds with the scripted result.
func replyOrRound(s *scripted) func(runner.Spec) runnertest.Answer {
	return func(spec runner.Spec) runnertest.Answer {
		if spec.Kind == "reply" {
			return runnertest.Answer{Result: &runner.Result{Reply: &runner.ReplyResult{FindingID: spec.Reply.Finding.ID, Text: "The issue is still present.", Action: "hold"}}}
		}
		return s.script(spec)
	}
}

func (e *env) comment(t *testing.T, n int, author, body string) int64 {
	t.Helper()
	id, code := e.GitHub.AddIssueComment("acme/payments", n, author, body)
	if code != http.StatusOK {
		t.Fatalf("comment %q: %d", body, code)
	}
	return id
}

func (e *env) botIssueComments(n int) []string {
	var out []string
	for _, c := range e.GitHub.IssueComments("acme/payments", n) {
		if strings.HasSuffix(c.Author, "[bot]") {
			out = append(out, c.Body)
		}
	}
	return out
}

func TestSCN_LIFE_003_DoneHeadNeedsCommandOrRerequest(t *testing.T) {
	e := start(t, nil, nil)
	pr := e.open(t, "acme/payments")
	waitFor(t, 60*time.Second, "round 1", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	head := e.head(pr.Number)
	ctx := context.Background()
	id := e.wfID("acme/payments", pr.Number)
	// A reconcile signal for the done head starts no round.
	if err := e.Temporal.SignalWorkflow(ctx, id, "", signals.SignalHeadUpdated, signals.Signal{Trigger: signals.TriggerReconcile, HeadSHA: head}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	if n := e.rounds(t, "acme/payments", pr.Number, ""); n != 1 {
		t.Fatalf("rounds after reconcile: %d", n)
	}
	// A check re-request starts one.
	if err := e.Temporal.SignalWorkflow(ctx, id, "", signals.SignalHeadUpdated, signals.Signal{Trigger: signals.TriggerCheckRerequest, HeadSHA: head}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 60*time.Second, "round 2", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 2 })
	// /sayso review starts one.
	e.comment(t, pr.Number, "dev-alice", "/sayso review")
	waitFor(t, 60*time.Second, "round 3", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 3 })
	if n := e.Count(t, `SELECT count(DISTINCT head_sha) FROM rounds`); n != 1 {
		t.Fatalf("heads: %d", n)
	}
}

func TestSCN_LIFE_004_PauseStopsRounds(t *testing.T) {
	e := start(t, nil, nil)
	pr := e.open(t, "acme/payments")
	waitFor(t, 60*time.Second, "round 1", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	n := 1
	push := func(want int, what string) {
		t.Helper()
		e.push(t, pr.Number, map[string]string{"CHANGES.md": what + "\n"})
		if want > n {
			n = want
			waitFor(t, 60*time.Second, what, func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == want })
			return
		}
		time.Sleep(4 * time.Second)
		if got := e.rounds(t, "acme/payments", pr.Number, ""); got != n {
			t.Fatalf("%s: %d rounds, want %d", what, got, n)
		}
	}
	// The skip label.
	e.GitHub.SetPull("acme/payments", pr.Number, func(p *fakegh.Pull) { p.Labels = []string{"sayso:skip"} })
	push(1, "push with the skip label")
	e.GitHub.SetPull("acme/payments", pr.Number, func(p *fakegh.Pull) { p.Labels = nil })
	push(2, "push without the label")
	// A draft, then ready for review.
	e.GitHub.SetPull("acme/payments", pr.Number, func(p *fakegh.Pull) { p.Draft = true })
	if _, code := e.GitHub.PullEvent("acme/payments", pr.Number, "converted_to_draft", nil); code != http.StatusOK {
		t.Fatal(code)
	}
	push(2, "push on a draft")
	e.GitHub.SetPull("acme/payments", pr.Number, func(p *fakegh.Pull) { p.Draft = false })
	if _, code := e.GitHub.PullEvent("acme/payments", pr.Number, "ready_for_review", nil); code != http.StatusOK {
		t.Fatal(code)
	}
	waitFor(t, 60*time.Second, "round after ready", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 3 })
	n = 3
	// /sayso pause, then /sayso resume.
	e.comment(t, pr.Number, "dev-alice", "/sayso pause")
	waitFor(t, 30*time.Second, "paused", func() bool { return e.Count(t, `SELECT count(*) FROM reviews WHERE state = 'paused'`) == 1 })
	push(3, "push while paused")
	e.comment(t, pr.Number, "dev-alice", "/sayso resume")
	waitFor(t, 30*time.Second, "resumed", func() bool { return e.Count(t, `SELECT count(*) FROM reviews WHERE state = 'open'`) == 1 })
	push(4, "push after resume")
}

func TestSCN_LIFE_005_RepliesWaitCommandsDoNot(t *testing.T) {
	s := &scripted{}
	hold := make(chan struct{})
	var mu sync.Mutex
	holding := false
	e := start(t, func(spec runner.Spec) runnertest.Answer {
		a := replyOrRound(s)(spec)
		mu.Lock()
		defer mu.Unlock()
		if spec.Kind == "round" && holding {
			a.Hold = hold
		}
		return a
	}, nil)
	s.set(withFindings(rounding(3)))
	pr := e.openWith(t, splitFile("func Split() {}", "// a", "// b"))
	waitFor(t, 60*time.Second, "round 1", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	top := e.sayso(pr.Number)[0]
	// Round 2 runs and holds; a second finding gets its own thread later.
	mu.Lock()
	holding = true
	mu.Unlock()
	e.push(t, pr.Number, splitFile("func Split() {}", "// a", "// b", "// c"))
	waitFor(t, 60*time.Second, "round 2 running", func() bool { return e.sr.Running() == 1 })
	// A thread reply and a dismiss command arrive during the round.
	if _, code := e.GitHub.AddReviewComment("acme/payments", pr.Number, "dev-alice", "Is this still a problem?", "", 0, top.ID); code != http.StatusOK {
		t.Fatal(code)
	}
	e.comment(t, pr.Number, "dev-alice", "/sayso dismiss S-1 the remainder is added in the caller")
	waitFor(t, 30*time.Second, "the dismiss", func() bool {
		return e.Count(t, `SELECT count(*) FROM findings WHERE seq = 1 AND status = 'dismissed'`) == 1
	})
	if n := e.Count(t, `SELECT count(*) FROM agent_work WHERE status = 'queued'`); n != 1 {
		t.Fatalf("queued work while the round runs: %d", n)
	}
	if n := e.Count(t, `SELECT count(*) FROM agent_runs WHERE kind = 'reply'`); n != 0 {
		t.Fatal("the reply run started during the round")
	}
	close(hold)
	waitFor(t, 60*time.Second, "the reply after the round", func() bool { return e.Count(t, `SELECT count(*) FROM agent_work WHERE status = 'done'`) == 1 })
	if n := e.Count(t, `SELECT count(*) FROM agent_runs r, rounds ro WHERE r.kind = 'reply' AND ro.status = 'done' AND ro.head_sha = $1 AND r.created_at >= ro.finished_at`, e.head(pr.Number)); n != 1 {
		t.Fatal("the reply run did not start after the round ended")
	}
	var replies int
	for _, c := range e.GitHub.Thread(top.ID) {
		if strings.HasSuffix(c.Author, "[bot]") && c.InReplyTo == top.ID && c.Body == "The issue is still present." {
			replies++
		}
	}
	if replies != 1 {
		t.Fatalf("Sayso replies in the thread: %d", replies)
	}
}
