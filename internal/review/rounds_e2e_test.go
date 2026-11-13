//go:build e2e

package review_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/audit"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	fakemodel "github.com/alternayte/sayso/internal/fakes/model"
	"github.com/alternayte/sayso/internal/publish"
	"github.com/alternayte/sayso/internal/runner"
)

const (
	agentImage    = "sayso-agent:dev"
	containerHost = "host.docker.internal"
)

// real starts the API, the workers and the fakes, with the process runner
// starting the real agent image (D-123).
func real(t *testing.T, mode string) *env {
	t.Helper()
	e := apptest.New(t, apptest.Options{Temporal: true, ContainerHost: containerHost})
	ctx := context.Background()
	fm := fakemodel.New()
	ms := httptest.NewServer(fm.Handler())
	t.Cleanup(ms.Close)
	if _, code := e.GitHub.Install("acme/payments", "acme/web"); code != http.StatusOK {
		t.Fatalf("install: %d", code)
	}
	e.SQL(t, `UPDATE repositories SET mode = $1, execution = false`, mode)
	e.SQL(t, `INSERT INTO providers (name, api_type, base_url, auth_style) VALUES ('fake', 'openai-completions', $1, 'bearer')`, ms.URL+"/v1")
	e.SQL(t, `UPDATE profiles SET targets = '[{"provider":"fake","model":"fake-large","price_in":0,"price_out":0,"price_cache_read":0}]'`)
	for k, v := range map[string]any{"debounce_seconds": 1, "github_web_url": e.ContainerURL(containerHost, e.GHServer)} {
		raw, _ := json.Marshal(v)
		if _, err := e.F.Settings.Set(ctx, audit.System, k, raw); err != nil {
			t.Fatal(err)
		}
	}
	logs, err := os.CreateTemp(t.TempDir(), "agent-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd := []string{"docker", "run", "--rm", "--init", "--add-host=" + containerHost + ":host-gateway", "-e", "SAYSO_API_URL", "-e", "SAYSO_RUN_TOKEN", "-e", "SAYSO_RUN_ID", agentImage, "agent"}
	quoted := make([]string, len(cmd))
	for i, a := range cmd {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	e.Deps.Runner = &runner.Process{Command: []string{"/bin/sh", "-c", strings.Join(quoted, " ") + " >>" + logs.Name() + " 2>&1"},
		Getenv: func(k string) string {
			if k == "PATH" {
				return os.Getenv("PATH")
			}
			return ""
		}}
	stop, err := app.StartWorker(ctx, e.Deps, e.F)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	setDump(e)
	t.Cleanup(func() {
		if t.Failed() {
			b, _ := os.ReadFile(logs.Name())
			t.Logf("agent logs:\n%s\nstate:\n%s", b, dump())
		}
	})
	return &env{Env: e}
}

// script commits a .sayso.yml that selects a fake model script, then opens
// a pull request with files.
func (e *env) script(t *testing.T, name string, files map[string]string) *fakegh.Pull {
	t.Helper()
	e.GitHub.CommitMain("acme/payments", map[string]string{".sayso.yml": "instructions: \"[script:" + name + "]\"\n"})
	return e.openWith(t, files)
}

const noCheck = `// Package ledger records payments.
package ledger

import "errors"

// ErrNegative is returned for a negative amount.
var ErrNegative = errors.New("ledger: negative amount")

// Ledger holds the balance of one account in cents.
type Ledger struct {
	balance int64
}

// Post adds amount cents to the balance.
func (l *Ledger) Post(amount int64) error {
	// Refunds pass negative amounts now.
	_ = ErrNegative
	_ = amount
	l.balance += amount
	return nil
}

// Balance returns the balance in cents.
func (l *Ledger) Balance() int64 { return l.balance }
`

func (e *env) summary(t *testing.T, n int) fakegh.IssueComment {
	t.Helper()
	for _, c := range e.GitHub.IssueComments("acme/payments", n) {
		if strings.HasPrefix(c.Body, publish.SummaryMarker) {
			return c
		}
	}
	return fakegh.IssueComment{}
}

func (e *env) check(head string) fakegh.CheckRun {
	var out fakegh.CheckRun
	for _, c := range e.GitHub.CheckRuns() {
		if c.HeadSHA == head {
			out = c
		}
	}
	return out
}

func TestSCN_E2E_001_GateBlocksThenFixPasses(t *testing.T) {
	e := real(t, "gate")
	pr := e.script(t, "e2e-caller", map[string]string{"ledger/ledger.go": noCheck})
	waitFor(t, 4*time.Minute, "round 1", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	s := e.summary(t, pr.Number).Body
	if !strings.Contains(s, "**Impact**") || !strings.Contains(s, "`api/handler.go`") {
		t.Fatalf("summary has no caller in Impact:\n%s", s)
	}
	if n := e.Count(t, `SELECT count(*) FROM findings WHERE blocking AND status = 'open' AND evidence->>'kind' = 'call_path'`); n != 1 {
		t.Fatalf("blocking call_path findings: %d", n)
	}
	if c := e.check(e.head(pr.Number)); c.Conclusion != "failure" {
		t.Fatalf("check %+v", c)
	}
	// A push that fixes it: the finding is fixed, the check passes.
	fixed := strings.Replace(noCheck, "\t// Refunds pass negative amounts now.\n\t_ = ErrNegative\n\t_ = amount\n", "\tif amount < 0 {\n\t\treturn ErrNegative\n\t}\n", 1)
	e.push(t, pr.Number, map[string]string{"ledger/ledger.go": fixed})
	waitFor(t, 4*time.Minute, "round 2", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 2 })
	if n := e.Count(t, `SELECT count(*) FROM findings WHERE status = 'fixed'`); n != 1 {
		t.Fatalf("fixed findings: %d", n)
	}
	if c := e.check(e.head(pr.Number)); c.Conclusion != "success" {
		t.Fatalf("check after the fix %+v", c)
	}
}

func TestSCN_LIFE_008_FailedRoundIsNeutral(t *testing.T) {
	e := real(t, "gate")
	pr := e.script(t, "e2e-incomplete", nil)
	waitFor(t, 4*time.Minute, "a failed round", func() bool { return e.rounds(t, "acme/payments", pr.Number, "failed") == 1 })
	waitFor(t, 30*time.Second, "a neutral check", func() bool {
		c := e.check(e.head(pr.Number))
		return c.Status == "completed" && c.Conclusion == "neutral" && strings.Contains(c.Summary, "incomplete_output")
	})
	if s := e.summary(t, pr.Number).Body; !strings.Contains(s, "Review failed") {
		t.Fatalf("summary:\n%s", s)
	}
}

func TestSCN_PUB_002_003_SummaryInPlaceAndInlineLimit(t *testing.T) {
	e := real(t, "comment")
	e.SQL(t, `UPDATE repositories SET challenge_min_severity = 'blocker'`)
	fees := "package ledger\n\n"
	for i := 0; i < 20; i++ {
		fees += "// fee line\n"
	}
	pr := e.script(t, "e2e-many", map[string]string{"ledger/fees.go": fees})
	waitFor(t, 4*time.Minute, "round 1", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	var reviews []fakegh.Review
	for _, r := range e.GitHub.Reviews() {
		if r.Number == pr.Number {
			reviews = append(reviews, r)
		}
	}
	if len(reviews) != 1 || reviews[0].Event != "COMMENT" || len(reviews[0].Comments) != 10 {
		t.Fatalf("reviews %+v", reviews)
	}
	for _, c := range e.sayso(pr.Number) {
		if !strings.Contains(c.Body, "<!-- sayso:finding:") || strings.Contains(c.Body, "· nit") || c.Path == "api/handler.go" {
			t.Fatalf("inline comment %+v", c)
		}
	}
	s := e.summary(t, pr.Number)
	for _, want := range []string{"Trailing whitespace", "Odd brace spacing", "Comment typo cents", "Handler ignores new fee"} {
		if !strings.Contains(s.Body, want) {
			t.Fatalf("the summary has no %q", want)
		}
	}
	var ms float64
	if err := e.Deps.Pool.QueryRow(context.Background(), `SELECT extract(epoch FROM (ro.finished_at - ar.finished_at)) * 1000 FROM rounds ro JOIN agent_runs ar ON ar.id = ro.agent_run_id`).Scan(&ms); err != nil {
		t.Fatal(err)
	}
	if ms > 15000 {
		t.Fatalf("publish took %.0f ms", ms)
	}
	// SCN-PUB-002: a second round edits the one summary in place.
	e.push(t, pr.Number, map[string]string{"ledger/fees.go": fees + "// more\n"})
	waitFor(t, 4*time.Minute, "round 2", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 2 })
	n := 0
	var body string
	var edits int
	for _, c := range e.GitHub.IssueComments("acme/payments", pr.Number) {
		if strings.HasPrefix(c.Body, publish.SummaryMarker) {
			n++
			body, edits = c.Body, c.Edits
		}
	}
	if n != 1 || edits < 2 {
		t.Fatalf("%d summaries, %d edits", n, edits)
	}
	order := []string{"Reviewed " + e.head(pr.Number)[:7], "**Verdict:**", "**TL;DR:**", "**Findings:**", "| ID |", "Commands:"}
	at := -1
	for _, part := range order {
		i := strings.Index(body, part)
		if i <= at {
			t.Fatalf("section %q is out of order:\n%s", part, body)
		}
		at = i
	}
}

func TestSCN_PUB_006_StartedRoundShows(t *testing.T) {
	e := real(t, "gate")
	pr := e.script(t, "e2e-slow-start", nil)
	// While the agent run works, the check is in progress.
	waitFor(t, 2*time.Minute, "an in-progress check during the agent run", func() bool {
		c := e.check(e.head(pr.Number))
		return c.Status == "in_progress" && c.Title == "Reviewing "+e.head(pr.Number)[:7] &&
			e.Count(t, `SELECT count(*) FROM agent_runs WHERE status = 'running'`) == 1
	})
	if s := e.summary(t, pr.Number).Body; !strings.Contains(s, "Reviewing "+e.head(pr.Number)[:7]) {
		t.Fatalf("status line:\n%s", s)
	}
	eyes := false
	for _, r := range e.GitHub.Reactions() {
		if r.Number == pr.Number && r.Content == "eyes" && strings.HasSuffix(r.Author, "[bot]") {
			eyes = true
		}
	}
	if !eyes {
		t.Fatal("no eyes reaction from the App")
	}
	waitFor(t, 4*time.Minute, "the round ends", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
}

func TestSCN_FND_006_RefutedAndDowngraded(t *testing.T) {
	e := real(t, "gate")
	pr := e.script(t, "e2e-challenge-outcomes", map[string]string{"ledger/ledger.go": noCheck})
	waitFor(t, 4*time.Minute, "round 1", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	if n := e.Count(t, `SELECT count(*) FROM findings WHERE title = 'Overflow on large amounts' AND status = 'withdrawn' AND comment_id IS NULL`); n != 1 {
		t.Fatal("the refuted finding is not withdrawn and unposted")
	}
	if n := e.Count(t, `SELECT count(*) FROM findings WHERE title = 'Negative amounts pass' AND severity = 'major' AND NOT blocking AND comment_id IS NOT NULL`); n != 1 {
		t.Fatal("the downgraded finding is not a posted, non-blocking major")
	}
	if c := e.check(e.head(pr.Number)); c.Conclusion != "neutral" {
		t.Fatalf("check %+v", c)
	}
}

func TestSCN_PUB_008_AgentTextIsSafeOnGitHub(t *testing.T) {
	e := real(t, "comment")
	pr := e.script(t, "e2e-unsafe-text", map[string]string{"ledger/ledger.go": noCheck})
	waitFor(t, 4*time.Minute, "round 1", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	cs := e.sayso(pr.Number)
	if len(cs) != 1 {
		t.Fatalf("comments: %d", len(cs))
	}
	for _, body := range []string{cs[0].Body, e.summary(t, pr.Number).Body} {
		if !strings.Contains(body, "`@octo-admin`") || strings.Contains(body, " @octo-admin") || strings.Contains(body, "<script>") {
			t.Fatalf("unsafe text:\n%s", body)
		}
	}
}
