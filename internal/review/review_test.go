package review_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/audit"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/review"
	"github.com/alternayte/sayso/internal/review/signals"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/runner/runnertest"
)

type env struct {
	*apptest.Env
	sr *runnertest.Scripted
}

// lookGood is a result with an assessment and no findings.
func lookGood() *runner.Result {
	return &runner.Result{Assessment: &runner.Assessment{TLDR: "The change is small.", Risk: "low", RiskReasons: []string{"small change"},
		WhereToLook: []runner.WhereToLook{}, Impact: []runner.Impact{}}}
}

// start runs the API, the fakes and the workers. The settings map sets
// numbers such as the debounce.
func start(t *testing.T, script func(runner.Spec) runnertest.Answer, set map[string]any) *env {
	t.Helper()
	e := apptest.New(t, apptest.Options{Temporal: true})
	ctx := context.Background()
	if _, code := e.GitHub.Install("acme/payments", "acme/web"); code != http.StatusOK {
		t.Fatalf("install: %d", code)
	}
	e.SQL(t, `UPDATE repositories SET mode = 'gate'`)
	e.SQL(t, `INSERT INTO providers (name, api_type, base_url, auth_style) VALUES ('fake', 'openai-completions', 'http://127.0.0.1:1/v1', 'bearer')`)
	e.SQL(t, `UPDATE profiles SET targets = '[{"provider":"fake","model":"fake-large","price_in":0,"price_out":0,"price_cache_read":0}]'`)
	if set == nil {
		set = map[string]any{}
	}
	if _, ok := set["debounce_seconds"]; !ok {
		set["debounce_seconds"] = 1
	}
	for k, v := range set {
		raw, _ := json.Marshal(v)
		if _, err := e.F.Settings.Set(ctx, audit.System, k, raw); err != nil {
			t.Fatalf("setting %s: %v", k, err)
		}
	}
	if script == nil {
		script = func(runner.Spec) runnertest.Answer { return runnertest.Answer{Result: lookGood()} }
	}
	sr := &runnertest.Scripted{Script: script}
	e.Deps.Runner = sr
	stop, err := app.StartWorker(ctx, e.Deps, e.F)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	if dir := os.Getenv("SAYSO_RECORD_REPLAY"); dir != "" {
		since := time.Now()
		t.Cleanup(func() { record(t, e, dir, since) })
	}
	setDump(e)
	return &env{Env: e, sr: sr}
}

// setDump makes failed waits print the rounds, runs and workflows.
func setDump(e *apptest.Env) {
	ctx := context.Background()
	dump = func() string {
		var out string
		rows, err := e.Deps.Pool.Query(ctx, `SELECT ro.status, ro.failure_reason, coalesce(ar.status, '-'), coalesce(ar.exit_reason, '-') FROM rounds ro
			LEFT JOIN agent_runs ar ON ar.id = ro.agent_run_id ORDER BY ro.created_at`)
		if err != nil {
			return err.Error()
		}
		defer rows.Close()
		for rows.Next() {
			var a, b, c, d string
			_ = rows.Scan(&a, &b, &c, &d)
			out += "round " + a + " (" + b + ") run " + c + " (" + d + ")\n"
		}
		evs, _ := e.Deps.Pool.Query(ctx, `SELECT type, name, status, left(preview, 300) FROM run_events WHERE type IN ('tool_call', 'error', 'note') ORDER BY run_id, seq`)
		for evs.Next() {
			var a, b, c, d string
			_ = evs.Scan(&a, &b, &c, &d)
			out += "  event " + a + " " + b + " " + c + ": " + d + "\n"
		}
		evs.Close()
		rv, _ := e.Deps.Pool.Query(ctx, `SELECT workflow_id, state FROM reviews`)
		for rv.Next() {
			var id, st string
			_ = rv.Scan(&id, &st)
			out += "review " + id + " " + st + "\n"
			if d, err := e.Temporal.DescribeWorkflow(ctx, id, ""); err == nil {
				out += "  workflow " + d.RawResponse.WorkflowExecutionInfo.Status.String() + "\n"
				for _, p := range d.RawResponse.PendingActivities {
					out += "  pending " + p.ActivityType.GetName() + " attempt " + fmt.Sprint(p.Attempt) + ": " + p.GetLastFailure().GetMessage() + "\n"
				}
			}
		}
		rv.Close()
		return out
	}
}

func (e *env) open(t *testing.T, repo string) *fakegh.Pull {
	t.Helper()
	pr := e.GitHub.OpenPull(repo, fakegh.PullOptions{Author: "dev-alice"})
	if _, code := e.GitHub.PullEvent(repo, pr.Number, "opened", nil); code != http.StatusOK {
		t.Fatalf("opened: %d", code)
	}
	return pr
}

// dump is the state that a failed wait prints.
var dump func() string

// waitFor polls a condition until it holds or the timeout ends.
func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			state := ""
			if dump != nil {
				state = dump()
			}
			t.Fatalf("timed out: %s\n%s", what, state)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (e *env) rounds(t *testing.T, repo string, n int, status string) int {
	return e.Count(t, `SELECT count(*) FROM rounds ro JOIN reviews rv ON rv.id = ro.review_id JOIN repositories rp ON rp.id = rv.repository_id
		WHERE rp.full_name = $1 AND rv.pr_number = $2 AND ($3 = '' OR ro.status = $3)`, repo, n, status)
}

func (e *env) wfID(repo string, n int) string { return signals.WorkflowID(e.GitHub.Repo(repo).ID, n) }

func TestSCN_LIFE_001_OneWorkflowPerPullRequest(t *testing.T) {
	e := start(t, nil, nil)
	pr := e.GitHub.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice"})
	var wg sync.WaitGroup
	for _, action := range []string{"opened", "ready_for_review"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, code := e.GitHub.PullEvent("acme/payments", pr.Number, action, nil); code != http.StatusOK {
				t.Errorf("%s: %d", action, code)
			}
		}()
	}
	wg.Wait()
	waitFor(t, 60*time.Second, "one done round", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	if n := e.Count(t, `SELECT count(*) FROM reviews WHERE workflow_id = $1`, e.wfID("acme/payments", pr.Number)); n != 1 {
		t.Fatalf("reviews: %d", n)
	}
	d, err := e.Temporal.DescribeWorkflow(context.Background(), e.wfID("acme/payments", pr.Number), "")
	if err != nil {
		t.Fatal(err)
	}
	if d.RawResponse.WorkflowExecutionInfo.Status != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
		t.Fatalf("workflow status %v", d.RawResponse.WorkflowExecutionInfo.Status)
	}
	if n := e.rounds(t, "acme/payments", pr.Number, ""); n != 1 {
		t.Fatalf("rounds: %d", n)
	}
}

func TestSCN_LIFE_002_DebounceAndSupersede(t *testing.T) {
	hold := make(chan struct{})
	held := true
	var mu sync.Mutex
	e := start(t, func(runner.Spec) runnertest.Answer {
		mu.Lock()
		defer mu.Unlock()
		if held {
			return runnertest.Answer{Result: lookGood(), Hold: hold}
		}
		return runnertest.Answer{Result: lookGood()}
	}, map[string]any{"debounce_seconds": 3})
	pr := e.open(t, "acme/payments")
	// Three pushes 1 s apart: one round, 3 s after the last push.
	var last time.Time
	for i := 0; i < 3; i++ {
		time.Sleep(time.Second)
		if _, code := e.GitHub.Push("acme/payments", pr.Number, false); code != http.StatusOK {
			t.Fatalf("push: %d", code)
		}
		last = time.Now()
	}
	waitFor(t, 60*time.Second, "a running round", func() bool { return e.rounds(t, "acme/payments", pr.Number, "running") == 1 })
	var created time.Time
	if err := e.Deps.Pool.QueryRow(context.Background(), `SELECT ro.created_at FROM rounds ro JOIN reviews rv ON rv.id = ro.review_id WHERE rv.pr_number = $1`, pr.Number).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if d := created.Sub(last); d < 3*time.Second-300*time.Millisecond {
		t.Fatalf("the round started %s after the last push", d)
	}
	if n := e.rounds(t, "acme/payments", pr.Number, ""); n != 1 {
		t.Fatalf("rounds after three pushes: %d", n)
	}
	waitFor(t, 30*time.Second, "the agent run", func() bool { return len(e.sr.Started()) == 1 })
	first := e.sr.Started()[0]
	// A push during the running round cancels it and starts a new one.
	mu.Lock()
	held = false
	mu.Unlock()
	if _, code := e.GitHub.Push("acme/payments", pr.Number, false); code != http.StatusOK {
		t.Fatalf("push: %d", code)
	}
	waitFor(t, 60*time.Second, "a cancelled and a done round", func() bool {
		return e.rounds(t, "acme/payments", pr.Number, "cancelled") == 1 && e.rounds(t, "acme/payments", pr.Number, "done") == 1
	})
	if !slices.Contains(e.sr.Stopped(), first) {
		t.Fatalf("the first agent run was not stopped: %v", e.sr.Stopped())
	}
	close(hold)
}

func TestSCN_LIFE_007_ContinueAsNew(t *testing.T) {
	e := start(t, nil, nil)
	pr := e.open(t, "acme/payments")
	waitFor(t, 60*time.Second, "a done round", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	// A draft pauses the review; the state lives in the review row and the workflow.
	if _, code := e.GitHub.PullEvent("acme/payments", pr.Number, "converted_to_draft", nil); code != http.StatusOK {
		t.Fatal(code)
	}
	id := e.wfID("acme/payments", pr.Number)
	ctx := context.Background()
	before, err := e.Temporal.DescribeWorkflow(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		if err := e.Temporal.SignalWorkflow(ctx, id, "", signals.SignalThreadState, signals.Signal{ThreadNodeID: "PRRT_none", Resolved: true}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 60*time.Second, "continue-as-new", func() bool {
		d, err := e.Temporal.DescribeWorkflow(ctx, id, "")
		return err == nil && d.RawResponse.WorkflowExecutionInfo.Execution.RunId != before.RawResponse.WorkflowExecutionInfo.Execution.RunId
	})
	// The new run keeps working: a push while paused starts no round,
	// ready_for_review starts one.
	e.GitHub.SetPull("acme/payments", pr.Number, func(p *fakegh.Pull) { p.Draft = false })
	if _, code := e.GitHub.Push("acme/payments", pr.Number, false); code != http.StatusOK {
		t.Fatal(code)
	}
	waitFor(t, 60*time.Second, "a second done round", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 2 })
	it := e.Temporal.GetWorkflowHistory(ctx, id, before.RawResponse.WorkflowExecutionInfo.Execution.RunId, false, enums.HISTORY_EVENT_FILTER_TYPE_CLOSE_EVENT)
	ev, err := it.Next()
	if err != nil || ev.GetWorkflowExecutionContinuedAsNewEventAttributes() == nil {
		t.Fatalf("the first run did not continue as new: %v %v", ev, err)
	}
}

func TestSCN_LIFE_009_WorkerLimit(t *testing.T) {
	hold := make(chan struct{})
	e := start(t, func(runner.Spec) runnertest.Answer { return runnertest.Answer{Result: lookGood(), Hold: hold} },
		map[string]any{"worker_max_agent_runs": 2, "max_concurrent_rounds": 5})
	for i := 0; i < 5; i++ {
		e.open(t, "acme/payments")
	}
	waitFor(t, 60*time.Second, "two running agent runs", func() bool { return e.sr.Running() == 2 })
	time.Sleep(3 * time.Second)
	if e.sr.Running() != 2 {
		t.Fatalf("running: %d", e.sr.Running())
	}
	close(hold)
	waitFor(t, 120*time.Second, "five done rounds", func() bool {
		return e.Count(t, `SELECT count(*) FROM rounds WHERE status = 'done'`) == 5
	})
	if m := e.sr.MaxRunning(); m > 2 {
		t.Fatalf("at most %d agent runs ran at the same time", m)
	}
}

func TestSCN_LIFE_011_FairAdmission(t *testing.T) {
	var mu sync.Mutex
	holds := map[string]chan struct{}{}
	e := start(t, func(s runner.Spec) runnertest.Answer {
		mu.Lock()
		defer mu.Unlock()
		h := make(chan struct{})
		holds[s.RunID] = h
		return runnertest.Answer{Result: lookGood(), Hold: h}
	}, map[string]any{"worker_max_agent_runs": 2, "max_concurrent_rounds": 2})
	for i := 0; i < 6; i++ {
		e.open(t, "acme/payments")
		time.Sleep(100 * time.Millisecond)
	}
	waitFor(t, 60*time.Second, "six waiting admissions", func() bool {
		return e.Count(t, `SELECT count(*) FROM admissions`) == 6
	})
	e.open(t, "acme/web")
	waitFor(t, 60*time.Second, "the acme/web admission", func() bool { return e.Count(t, `SELECT count(*) FROM admissions`) == 7 })
	// A reply run that arrives later goes before the waiting rounds.
	var repo uuid.UUID
	if err := e.Deps.Pool.QueryRow(context.Background(), `SELECT id FROM repositories WHERE full_name = 'acme/web'`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	a := app.ReviewActivities(e.Deps, e.F)
	reply := make(chan uuid.UUID, 1)
	go func() {
		id, err := a.Admit(context.Background(), review.AdmitInput{RunID: nil, RepositoryID: repo, Kind: review.KindReply})
		if err != nil {
			t.Error(err)
		}
		reply <- id
	}()
	waitFor(t, 30*time.Second, "the reply admission", func() bool { return e.Count(t, `SELECT count(*) FROM admissions WHERE kind = 'reply'`) == 1 })
	release := func() {
		mu.Lock()
		defer mu.Unlock()
		for id, h := range holds {
			close(h)
			delete(holds, id)
			return
		}
	}
	waitFor(t, 30*time.Second, "two running", func() bool { return e.sr.Running() == 2 })
	release()
	id := <-reply
	if err := a.Release(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	for e.Count(t, `SELECT count(*) FROM rounds WHERE status = 'done'`) < 7 {
		waitFor(t, 60*time.Second, "a running agent run", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(holds) > 0 || e.Count(t, `SELECT count(*) FROM rounds WHERE status = 'done'`) == 7
		})
		release()
		time.Sleep(200 * time.Millisecond)
	}
	// B's round is admitted before A's third round.
	var webAt, thirdPay time.Time
	ctx := context.Background()
	if err := e.Deps.Pool.QueryRow(ctx, `SELECT a.admitted_at FROM admissions a JOIN repositories r ON r.id = a.repository_id WHERE r.full_name = 'acme/web' AND a.kind = 'round'`).Scan(&webAt); err != nil {
		t.Fatal(err)
	}
	if err := e.Deps.Pool.QueryRow(ctx, `SELECT a.admitted_at FROM admissions a JOIN repositories r ON r.id = a.repository_id WHERE r.full_name = 'acme/payments'
		ORDER BY a.admitted_at OFFSET 2 LIMIT 1`).Scan(&thirdPay); err != nil {
		t.Fatal(err)
	}
	if !webAt.Before(thirdPay) {
		rows, _ := e.Deps.Pool.Query(ctx, `SELECT a.kind, r.full_name, a.admitted_at FROM admissions a JOIN repositories r ON r.id = a.repository_id ORDER BY a.admitted_at`)
		for rows.Next() {
			var k, n string
			var ad *time.Time
			_ = rows.Scan(&k, &n, &ad)
			t.Logf("%s %s admitted %v", k, n, ad)
		}
		t.Fatalf("acme/web was admitted at %s, after the third acme/payments round at %s", webAt, thirdPay)
	}
	var replyAt, thirdAt time.Time
	_ = e.Deps.Pool.QueryRow(context.Background(), `SELECT admitted_at FROM admissions WHERE kind = 'reply'`).Scan(&replyAt)
	_ = e.Deps.Pool.QueryRow(context.Background(), `SELECT admitted_at FROM admissions WHERE kind = 'round' ORDER BY admitted_at OFFSET 3 LIMIT 1`).Scan(&thirdAt)
	if !replyAt.Before(thirdAt) {
		t.Fatalf("the reply was admitted at %s, after the fourth round at %s", replyAt, thirdAt)
	}
}

// record writes the history of each workflow of a test to dir, for the
// replay test (§7.1, REQ-LIFE-013). Run the tests with SAYSO_RECORD_REPLAY
// after a change to workflow code.
func record(t *testing.T, e *apptest.Env, dir string, since time.Time) {
	ctx := context.Background()
	resp, err := e.Temporal.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{PageSize: 1000})
	if err != nil {
		t.Errorf("list workflows: %v", err)
		return
	}
	name := strings.TrimPrefix(t.Name(), "TestSCN_")
	for i, w := range resp.Executions {
		typ := w.Type.GetName()
		if (typ != signals.WorkflowType && typ != review.RoundWorkflowType && typ != review.WorkWorkflowType) || w.StartTime.AsTime().Before(since) {
			continue
		}
		it := e.Temporal.GetWorkflowHistory(ctx, w.Execution.WorkflowId, w.Execution.RunId, false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		h := &historypb.History{}
		for it.HasNext() {
			ev, err := it.Next()
			if err != nil {
				t.Errorf("history: %v", err)
				return
			}
			h.Events = append(h.Events, ev)
		}
		b, err := protojson.MarshalOptions{Indent: "  "}.Marshal(h)
		if err != nil {
			t.Error(err)
			return
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%s-%02d.json", name, typ, i)), b, 0o644); err != nil {
			t.Error(err)
		}
	}
}

func (e *env) head(n int) string { return e.GitHub.Pull("acme/payments", n).HeadSHA }
