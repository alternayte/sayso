package review_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.temporal.io/api/enums/v1"

	"github.com/alternayte/sayso/internal/audit"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/publish"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/runner/runnertest"
	"github.com/alternayte/sayso/internal/skills"
	"github.com/alternayte/sayso/internal/skills/skaeltest"
)

const splitPath = "internal/money/split.go"

func splitFile(lines ...string) map[string]string {
	return map[string]string{splitPath: "package money\n\n" + strings.Join(lines, "\n") + "\n"}
}

// scripted holds the result of the next round.
type scripted struct {
	mu   sync.Mutex
	next *runner.Result
}

func (s *scripted) set(r *runner.Result) {
	s.mu.Lock()
	s.next = r
	s.mu.Unlock()
}

func (s *scripted) script(runner.Spec) runnertest.Answer {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := lookGood()
	if s.next != nil {
		b, _ := json.Marshal(s.next)
		_ = json.Unmarshal(b, r)
	}
	return runnertest.Answer{Result: r}
}

func withFindings(fs ...runner.Finding) *runner.Result {
	r := lookGood()
	r.Findings = fs
	return r
}

func rounding(line int) runner.Finding {
	return runner.Finding{CandidateID: "c1", Severity: "major", Category: "correctness", Title: "Rounding drops a cent on split", Body: "Split loses one cent.",
		Path: splitPath, StartLine: line, EndLine: line, Evidence: runner.Evidence{Kind: "reasoning", Detail: "The remainder is not added back."}}
}

func (e *env) openWith(t *testing.T, files map[string]string) *fakegh.Pull {
	t.Helper()
	pr := e.GitHub.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice", Files: files})
	if _, code := e.GitHub.PullEvent("acme/payments", pr.Number, "opened", nil); code != http.StatusOK {
		t.Fatalf("opened: %d", code)
	}
	return pr
}

func (e *env) push(t *testing.T, n int, files map[string]string) {
	t.Helper()
	if _, code := e.GitHub.PushFiles("acme/payments", n, false, files); code != http.StatusOK {
		t.Fatalf("push: %d", code)
	}
}

// sayso returns the top-level Sayso review comments of a pull request.
func (e *env) sayso(n int) []fakegh.Comment {
	var out []fakegh.Comment
	for _, c := range e.GitHub.ReviewComments("acme/payments") {
		if c.Number == n && c.InReplyTo == 0 && strings.HasSuffix(c.Author, "[bot]") {
			out = append(out, c)
		}
	}
	return out
}

func (e *env) replies(n int, to int64) []fakegh.Comment {
	var out []fakegh.Comment
	for _, c := range e.GitHub.ReviewComments("acme/payments") {
		if c.Number == n && c.InReplyTo == to {
			out = append(out, c)
		}
	}
	return out
}

func (e *env) thread(n int, id int64) fakegh.Comment {
	for _, c := range e.GitHub.ReviewComments("acme/payments") {
		if c.Number == n && c.ID == id {
			return c
		}
	}
	return fakegh.Comment{}
}

func TestSCN_FND_004_005_RepeatMovesAndFixResolves(t *testing.T) {
	s := &scripted{}
	e := start(t, s.script, nil)
	s.set(withFindings(rounding(3)))
	pr := e.openWith(t, splitFile("func Split() {}", "// a", "// b", "// c"))
	waitFor(t, 60*time.Second, "round 1", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	top := e.sayso(pr.Number)
	if len(top) != 1 || !strings.Contains(top[0].Body, publish.FindingMarker("")[:len("<!-- sayso:finding:")]) || top[0].Line != 3 {
		t.Fatalf("round 1 comments: %+v", top)
	}

	// SCN-FND-004: same_as at a new line updates the location, posts nothing.
	moved := rounding(5)
	moved.SameAs = "S-1"
	s.set(withFindings(moved))
	e.push(t, pr.Number, splitFile("func Split() {}", "// x", "// a", "// b", "// c"))
	waitFor(t, 60*time.Second, "round 2", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 2 })
	if n := len(e.sayso(pr.Number)); n != 1 {
		t.Fatalf("comments after the repeat: %d", n)
	}
	if n := e.Count(t, `SELECT count(*) FROM findings WHERE seq = 1 AND start_line = 5 AND status = 'open'`); n != 1 {
		t.Fatal("the location did not move")
	}

	// SCN-FND-005: update_finding fixed replies, resolves and writes the event.
	fixed := lookGood()
	fixed.LedgerUpdates = []runner.LedgerUpdate{{FindingID: "S-1", Status: "fixed", Reason: "The remainder is added."}}
	s.set(fixed)
	e.push(t, pr.Number, splitFile("func Split() { add() }", "// x", "// a", "// b", "// c"))
	waitFor(t, 60*time.Second, "round 3", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 3 })
	head := e.GitHub.Repo("acme/payments").Pulls[pr.Number].HeadSHA
	rs := e.replies(pr.Number, top[0].ID)
	if len(rs) != 1 || rs[0].Body != "Fixed in "+head[:7]+"." {
		t.Fatalf("fixed replies: %+v", rs)
	}
	if !e.thread(pr.Number, top[0].ID).Resolved {
		t.Fatal("the thread is not resolved")
	}
	if n := e.Count(t, `SELECT count(*) FROM finding_events e JOIN findings f ON f.id = e.finding_id WHERE f.seq = 1 AND e.type = 'fixed' AND e.actor = 'agent'`); n != 1 {
		t.Fatalf("fixed events: %d", n)
	}
	var verdict string
	_ = e.Deps.Pool.QueryRow(context.Background(), `SELECT verdict FROM rounds WHERE head_sha = $1`, head).Scan(&verdict)
	if verdict != "looks_good" {
		t.Fatalf("verdict %q", verdict)
	}
}

func TestSCN_FND_003_DismissedRepeat(t *testing.T) {
	s := &scripted{}
	e := start(t, s.script, nil)
	s.set(withFindings(rounding(3)))
	pr := e.openWith(t, splitFile("func Split() {}", "// a", "// b"))
	waitFor(t, 60*time.Second, "round 1", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	top := e.sayso(pr.Number)
	e.SQL(t, `UPDATE findings SET status = 'dismissed' WHERE seq = 1`)

	// A repeat with reasoning evidence is suppressed; nothing is posted.
	again := rounding(3)
	again.Title = "Split rounding drops a cent"
	s.set(withFindings(again))
	e.push(t, pr.Number, splitFile("func Split() {}", "// a", "// b", "// c"))
	waitFor(t, 60*time.Second, "round 2", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 2 })
	if len(e.sayso(pr.Number)) != 1 || len(e.replies(pr.Number, top[0].ID)) != 0 {
		t.Fatal("the suppressed repeat posted something")
	}
	if n := e.Count(t, `SELECT count(*) FROM finding_events e JOIN findings f ON f.id = e.finding_id WHERE f.seq = 1 AND e.type = 'suppressed'`); n != 1 {
		t.Fatalf("suppressed events: %d", n)
	}

	// New test evidence reopens it with one reply; it does not block this round.
	proven := again
	proven.Severity = "blocker"
	proven.Evidence = runner.Evidence{Kind: "test", Detail: "TestSplitRemainder fails.", TestPath: ".sayso-tests/split_test.go"}
	proven.Challenge = &runner.Challenge{Outcome: "confirmed", Reason: "The test fails."}
	s.set(withFindings(proven))
	e.push(t, pr.Number, splitFile("func Split() {}", "// a", "// b", "// c", "// d"))
	waitFor(t, 60*time.Second, "round 3", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 3 })
	rs := e.replies(pr.Number, top[0].ID)
	if len(rs) != 1 || !strings.Contains(rs[0].Body, ".sayso-tests/split_test.go") {
		t.Fatalf("reopen replies: %+v", rs)
	}
	if n := e.Count(t, `SELECT count(*) FROM findings WHERE seq = 1 AND status = 'open' AND NOT blocking`); n != 1 {
		t.Fatal("the finding is not open and non-blocking")
	}
	if len(e.sayso(pr.Number)) != 1 {
		t.Fatal("the reopened finding got a new comment")
	}
}

func TestSCN_PUB_005_PublishRetryDuplicatesNothing(t *testing.T) {
	s := &scripted{}
	e := start(t, s.script, nil)
	s.set(withFindings(rounding(3)))
	// Each write of a gate round fails once in turn: check, summary,
	// reaction, review, summary edit, check update.
	for n := 1; n <= 6; n++ {
		e.GitHub.FailWrite(n)
		pr := e.openWith(t, splitFile("func Split() {}", "// a", "// b"))
		waitFor(t, 120*time.Second, "a done round", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
		head := e.GitHub.Repo("acme/payments").Pulls[pr.Number].HeadSHA
		summaries := 0
		for _, c := range e.GitHub.IssueComments("acme/payments", pr.Number) {
			if strings.HasPrefix(c.Body, publish.SummaryMarker) {
				summaries++
			}
		}
		reviews := 0
		for _, r := range e.GitHub.Reviews() {
			if r.Number == pr.Number {
				reviews++
			}
		}
		checks := 0
		for _, c := range e.GitHub.CheckRuns() {
			if c.HeadSHA == head {
				checks++
			}
		}
		if summaries != 1 || reviews != 1 || checks != 1 || len(e.sayso(pr.Number)) != 1 {
			t.Fatalf("write %d failed once: %d summaries, %d reviews, %d checks, %d comments", n, summaries, reviews, checks, len(e.sayso(pr.Number)))
		}
	}
}

func TestSCN_LIFE_006_CloseCancelsAndReports(t *testing.T) {
	sk := skaeltest.Start(t)
	hold := make(chan struct{})
	defer close(hold)
	manifest := &runner.SkaelManifest{Set: skaeltest.Set, ResolutionID: "r1", Skills: []runner.SkaelSkill{{Name: skaeltest.SkillName, Version: 1, SHA256: "x"}}}
	e := start(t, func(runner.Spec) runnertest.Answer {
		return runnertest.Answer{Result: lookGood(), Hold: hold, Manifest: manifest}
	}, map[string]any{"skael_url": sk.URL, "skael_default_set": skaeltest.Set})
	ctx := context.Background()
	for name, v := range map[string]string{skills.AgentKeySecret: sk.AgentKey, skills.ReporterKeySecret: sk.ReporterKey} {
		if _, err := e.F.Secrets.Put(ctx, audit.System, name, v); err != nil {
			t.Fatal(err)
		}
	}
	pr := e.open(t, "acme/payments")
	waitFor(t, 60*time.Second, "a running agent run", func() bool { return e.sr.Running() == 1 })
	time.Sleep(time.Second)
	e.GitHub.SetPull("acme/payments", pr.Number, func(p *fakegh.Pull) { p.State, p.Merged = "closed", true })
	if _, code := e.GitHub.PullEvent("acme/payments", pr.Number, "closed", nil); code != http.StatusOK {
		t.Fatal(code)
	}
	id := e.wfID("acme/payments", pr.Number)
	waitFor(t, 60*time.Second, "the workflow completes", func() bool {
		d, err := e.Temporal.DescribeWorkflow(ctx, id, "")
		return err == nil && d.RawResponse.WorkflowExecutionInfo.Status == enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
	})
	if e.rounds(t, "acme/payments", pr.Number, "cancelled") != 1 || e.Count(t, `SELECT count(*) FROM reviews WHERE state = 'merged'`) != 1 {
		t.Fatalf("round or review state:\n%s", dump())
	}
	if len(e.sr.Stopped()) != 1 {
		t.Fatal("the agent run was not stopped")
	}
	var round string
	_ = e.Deps.Pool.QueryRow(ctx, `SELECT id::text FROM rounds`).Scan(&round)
	if got := sk.Query(t, `SELECT outcome FROM runs WHERE consumer = 'sayso' AND external_run_id = '`+round+`'`); got != "cancelled" {
		t.Fatalf("Skael outcome %q", got)
	}
}

func TestSCN_LIFE_010_Budgets(t *testing.T) {
	var proxyStatus int
	var mu sync.Mutex
	e := start(t, func(runner.Spec) runnertest.Answer {
		return runnertest.Answer{ExitStatus: "failed", Reason: "the model call was refused", Before: func(j runner.Job, _ runner.Spec) {
			body, _ := json.Marshal(map[string]any{"model": "fake-large", "max_tokens": 100000, "messages": []map[string]string{{"role": "user", "content": "Review the change."}}})
			req, _ := http.NewRequest(http.MethodPost, j.APIURL+"/llm/fake/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+j.Token)
			req.Header.Set("Content-Type", "application/json")
			if resp, err := http.DefaultClient.Do(req); err == nil {
				mu.Lock()
				proxyStatus = resp.StatusCode
				mu.Unlock()
				_ = resp.Body.Close()
			}
		}}
	}, nil)
	e.SQL(t, `UPDATE profiles SET round_budget_usd = 0.01, targets = '[{"provider":"fake","model":"fake-large","max_tokens":100000,"price_in":1,"price_out":1,"price_cache_read":0}]'`)
	pr := e.open(t, "acme/payments")
	waitFor(t, 60*time.Second, "a budget_exceeded round", func() bool { return e.rounds(t, "acme/payments", pr.Number, "budget_exceeded") == 1 })
	mu.Lock()
	if proxyStatus != http.StatusPaymentRequired {
		t.Fatalf("the proxy answered %d", proxyStatus)
	}
	mu.Unlock()
	head := e.GitHub.Repo("acme/payments").Pulls[pr.Number].HeadSHA
	waitFor(t, 30*time.Second, "a neutral check", func() bool {
		for _, c := range e.GitHub.CheckRuns() {
			if c.HeadSHA == head && c.Status == "completed" {
				return c.Conclusion == "neutral"
			}
		}
		return false
	})

	// A spent monthly budget starts no new round and sets the banner flag.
	ctx := context.Background()
	if _, err := e.F.Settings.Set(ctx, audit.System, "monthly_budget_usd", json.RawMessage(`0.5`)); err != nil {
		t.Fatal(err)
	}
	e.SQL(t, `UPDATE agent_runs SET cost_usd = 1`)
	next := e.open(t, "acme/payments")
	waitFor(t, 30*time.Second, "the review of the second pull request", func() bool {
		return e.Count(t, `SELECT count(*) FROM reviews WHERE pr_number = $1 AND head_sha <> ''`, next.Number) == 1
	})
	time.Sleep(2 * time.Second)
	if n := e.rounds(t, "acme/payments", next.Number, ""); n != 0 {
		t.Fatalf("rounds with a spent monthly budget: %d", n)
	}
	var me struct {
		BudgetSpent bool `json:"budget_spent"`
	}
	c, _ := e.SignIn(t, "dev-bob")
	resp, err := c.Get(e.Base + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&me)
	_ = resp.Body.Close()
	if !me.BudgetSpent {
		t.Fatal("the banner flag is not set")
	}
}
