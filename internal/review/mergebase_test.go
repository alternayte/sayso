package review_test

import (
	"net/http"
	"sync"
	"testing"
	"time"

	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/runner/runnertest"
)

// The base of a round is the merge base, not the tip of the base branch, and
// a new base branch that moves the merge base starts a round.
func TestRoundBaseIsMergeBaseAndRetargetStartsRound(t *testing.T) {
	var mu sync.Mutex
	var bases []string
	e := start(t, func(s runner.Spec) runnertest.Answer {
		mu.Lock()
		bases = append(bases, s.BaseSHA)
		mu.Unlock()
		return runnertest.Answer{Result: lookGood()}
	}, nil)
	gh := e.GitHub
	initial := gh.MainSHA("acme/payments")
	gh.Branch("acme/payments", "release", initial)
	fork := gh.CommitMain("acme/payments", map[string]string{"api/limits.go": "package api\n"})
	pr := gh.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice"})
	// main moves after the branch point.
	tip := gh.CommitMain("acme/payments", map[string]string{"ledger/fees.go": "package ledger\n"})
	gh.SetPull("acme/payments", pr.Number, func(p *fakegh.Pull) { p.BaseSHA = tip })
	if _, code := gh.PullEvent("acme/payments", pr.Number, "opened", nil); code != http.StatusOK {
		t.Fatalf("opened: %d", code)
	}
	waitFor(t, 60*time.Second, "first round done", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })

	// A title edit changes nothing under review.
	if _, code := gh.PullEvent("acme/payments", pr.Number, "edited", map[string]any{"changes": map[string]any{"title": map[string]any{"from": "Old"}}}); code != http.StatusOK {
		t.Fatalf("edited title: %d", code)
	}
	gh.SetPull("acme/payments", pr.Number, func(p *fakegh.Pull) { p.BaseRef, p.BaseSHA = "release", initial })
	if _, code := gh.PullEvent("acme/payments", pr.Number, "edited", map[string]any{"changes": map[string]any{"base": map[string]any{"ref": map[string]any{"from": "main"}}}}); code != http.StatusOK {
		t.Fatalf("edited base: %d", code)
	}
	waitFor(t, 60*time.Second, "second round done", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 2 })
	if n := e.rounds(t, "acme/payments", pr.Number, ""); n != 2 {
		t.Fatalf("rounds: %d", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bases) != 2 || bases[0] != fork || bases[1] != initial {
		t.Fatalf("round bases %v, want [%s %s] (main tip %s)", bases, fork, initial, tip)
	}
	if n := e.Count(t, `SELECT count(*) FROM rounds WHERE trigger = 'base_changed' AND base_sha = $1`, initial); n != 1 {
		t.Fatalf("base_changed rounds: %d", n)
	}
}
