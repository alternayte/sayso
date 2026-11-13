package review_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/review/signals"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/runner/runnertest"
)

func TestSCN_OBS_006_SignalSources(t *testing.T) {
	s := &scripted{}
	e := start(t, func(spec runner.Spec) runnertest.Answer {
		if spec.Kind == "reply" {
			return runnertest.Answer{Result: &runner.Result{Reply: &runner.ReplyResult{FindingID: spec.Reply.Finding.ID, Text: "You are right.", Action: "concede"}}}
		}
		return s.script(spec)
	}, nil)
	f := func(line int, cat, title string) runner.Finding {
		x := rounding(line)
		x.Category, x.Title = cat, title
		return x
	}
	s.set(withFindings(f(3, "a", "Rounding drops a cent"), f(4, "b", "Split ignores negatives"), f(5, "c", "Remainder is lost")))
	pr := e.openWith(t, splitFile("func Split() {}", "// a", "// b", "// c"))
	waitFor(t, 60*time.Second, "round 1", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	threads := map[int]fakegh.Comment{}
	for _, c := range e.sayso(pr.Number) {
		threads[c.Line] = c
	}
	// dismiss and override commands.
	e.comment(t, pr.Number, "dev-alice", "/sayso dismiss S-1 the caller adds it back")
	e.comment(t, pr.Number, "octo-admin", "/sayso override the release owner accepts it")
	// A reply that the reply stage concedes.
	e.reply(t, pr.Number, threads[4].ID, "dev-alice", "Negatives never reach Split.")
	waitFor(t, 60*time.Second, "the concede", func() bool {
		return e.Count(t, `SELECT count(*) FROM findings WHERE seq = 2 AND status = 'conceded'`) == 1
	})
	// A person resolves S-3, and the next round reports it again.
	var thread string
	if err := e.Deps.Pool.QueryRow(context.Background(), `SELECT thread_node_id FROM findings WHERE seq = 3`).Scan(&thread); err != nil {
		t.Fatal(err)
	}
	if err := e.Temporal.SignalWorkflow(context.Background(), e.wfID("acme/payments", pr.Number), "", signals.SignalThreadState,
		signals.Signal{ThreadNodeID: thread, Resolved: true, Actor: "dev-alice"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "resolved by a person", func() bool {
		return e.Count(t, `SELECT count(*) FROM findings WHERE seq = 3 AND status = 'resolved_by_human'`) == 1
	})
	again := f(5, "c", "Remainder is lost")
	again.SameAs = "S-3"
	s.set(withFindings(again))
	// A rerun command starts the next round.
	e.comment(t, pr.Number, "dev-alice", "/sayso review")
	waitFor(t, 60*time.Second, "round 2", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 2 })
	// A person comments on a line with no finding.
	if _, code := e.GitHub.AddReviewComment("acme/payments", pr.Number, "dev-bob", "Why this name?", splitPath, 1, 0); code != http.StatusOK {
		t.Fatal(code)
	}
	for _, typ := range []string{"dismissed", "overridden", "conceded", "resolved_unfixed", "human_comment_unflagged", "rerun_requested"} {
		waitFor(t, 30*time.Second, "signal "+typ, func() bool { return e.Count(t, `SELECT count(*) FROM steering_signals WHERE type = $1`, typ) == 1 })
	}
}
