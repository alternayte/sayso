//go:build contract

package github_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/alternayte/sayso/internal/app/apptest"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/review"
)

func TestSCN_GH_007_ReconcilerRedeliversAndStartsRounds(t *testing.T) {
	e := apptest.New(t, apptest.Options{Temporal: true})
	installAcme(t, e)
	gh := e.GitHub

	// One failed delivery: the API was not reachable.
	hook := e.Base + "/webhooks/github"
	gh.SetHook("http://127.0.0.1:1/webhooks/github", apptest.WebhookSecret)
	pr := gh.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice"})
	failed, code := gh.PullEvent("acme/payments", pr.Number, "labeled", nil)
	if code != 0 {
		t.Fatalf("the failed delivery got status %d", code)
	}
	gh.SetHook(hook, apptest.WebhookSecret)
	// One open pull request with no round, and one with a done round for its head.
	done := gh.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice"})
	e.SQL(t, `INSERT INTO reviews (id, repository_id, pr_number, workflow_id) SELECT gen_random_uuid(), id, $1, 'x' FROM repositories WHERE full_name = 'acme/payments'`, done.Number)
	e.SQL(t, `INSERT INTO rounds (id, review_id, head_sha, base_sha, trigger, status) SELECT gen_random_uuid(), id, $1, $2, 'opened', 'done' FROM reviews WHERE pr_number = $3`, done.HeadSHA, done.BaseSHA, done.Number)
	// Draft pull requests and pull requests in repositories in mode off get none.
	gh.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice", Draft: true})
	e.SQL(t, `UPDATE repositories SET mode = 'off' WHERE full_name = 'acme/web'`)
	gh.OpenPull("acme/web", fakegh.PullOptions{Author: "dev-alice"})

	w := worker.New(e.Temporal, review.TaskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(github.Reconciler, workflow.RegisterOptions{Name: github.ReconcilerWorkflow})
	w.RegisterActivity(&github.ReconcileActivities{Pool: e.Deps.Pool, Secrets: e.F.Secrets, GitHub: e.F.GitHubClient, Temporal: e.Temporal})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	run, err := e.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "reconciler-test", TaskQueue: review.TaskQueue}, github.ReconcilerWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var res github.ReconcileResult
	if err := run.Get(ctx, &res); err != nil {
		t.Fatal(err)
	}
	if res.Redelivered != 1 || res.Started != 1 {
		t.Fatalf("result %+v", res)
	}
	attempts := 0
	for _, r := range gh.Requests() {
		if r.Method == http.MethodPost && strings.HasPrefix(r.Path, "/app/hook/deliveries/") && strings.HasSuffix(r.Path, "/attempts") {
			attempts++
		}
	}
	if attempts != 1 {
		t.Fatalf("redelivery requests: %d", attempts)
	}
	got := signals(t, e.Temporal, wfID(e, "acme/payments", pr.Number))
	if len(got) != 1 || got[0].Name != review.SignalHeadUpdated || got[0].Trigger != review.TriggerReconcile || got[0].HeadSHA != pr.HeadSHA {
		t.Fatalf("signals: %+v", got)
	}
	if exists(t, e.Temporal, wfID(e, "acme/payments", done.Number)) {
		t.Fatal("a pull request with a done round got a signal")
	}
	// The redelivery arrives and is recorded as a redelivery.
	deadline := time.Now().Add(10 * time.Second)
	for e.Count(t, `SELECT count(*) FROM webhook_deliveries WHERE delivery_id = $1 AND received_via = 'redelivery'`, failed) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the redelivery was not recorded")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// A second run finds nothing to do.
	run, err = e.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "reconciler-test-2", TaskQueue: review.TaskQueue}, github.ReconcilerWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Get(ctx, &res); err != nil {
		t.Fatal(err)
	}
	if res.Redelivered != 0 {
		t.Fatalf("second run redelivered %d", res.Redelivered)
	}
	// The schedule exists with the interval and changes with it.
	if err := github.EnsureReconcileSchedule(ctx, e.Temporal, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := github.EnsureReconcileSchedule(ctx, e.Temporal, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	desc, err := e.Temporal.ScheduleClient().GetHandle(ctx, github.ReconcilerSchedule).Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if iv := desc.Schedule.Spec.Intervals; len(iv) != 1 || iv[0].Every != 5*time.Minute {
		t.Fatalf("schedule intervals %+v", iv)
	}
}

func TestSCN_GH_008_TokensNarrowedAndMintedPerActivity(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	ctx := context.Background()
	app, err := github.LoadApp(ctx, e.Deps.Pool, e.F.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	gh, err := e.F.GitHubClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gh.InstallationToken(ctx, app, e.GitHub.InstallID, github.TokenRequest{Repositories: []string{"payments"}, Permissions: github.ReadOnlyRun}); err != nil {
		t.Fatal(err)
	}
	grants := e.GitHub.Grants()
	g := grants[len(grants)-1]
	if len(g.Repositories) != 1 || g.Repositories[0] != "payments" || len(g.Permissions) != 3 ||
		g.Permissions["contents"] != "read" || g.Permissions["pull_requests"] != "read" || g.Permissions["metadata"] != "read" {
		t.Fatalf("agent run token grant %+v", g)
	}

	// The token cache gives a token for at most 50 minutes.
	clock := &testClock{now: time.Now()}
	tokens := github.NewTokens()
	tokens.Now = clock.Now
	before := len(e.GitHub.Grants())
	first, err := tokens.Get(ctx, gh, app, e.GitHub.InstallID)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(20 * time.Minute)
	again, _ := tokens.Get(ctx, gh, app, e.GitHub.InstallID)
	if again != first || len(e.GitHub.Grants()) != before+1 {
		t.Fatal("a token was minted again within 50 minutes")
	}
	clock.Advance(35 * time.Minute)
	later, _ := tokens.Get(ctx, gh, app, e.GitHub.InstallID)
	if later == first || len(e.GitHub.Grants()) != before+2 {
		t.Fatal("an activity after 55 minutes got the old token")
	}
}

// testClock is a manual clock for the rate limiter.
type testClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, waiter{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			kept = append(kept, w)
		}
	}
	c.waiters = kept
}

func (c *testClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

func TestSCN_GH_011_RateLimitsPerInstallation(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	limits := github.NewRateLimits(clock)
	gh, err := e.F.GitHubClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gh.Limits = limits
	app, err := github.LoadApp(ctx, e.Deps.Pool, e.F.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	inst := e.GitHub.InstallID
	tok, err := gh.InstallationToken(ctx, app, inst, github.TokenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	run := e.GitHub.AddCheckRun("acme/payments", "sayso", "abc")

	// 5 % remaining, reset in 60 s.
	e.GitHub.SetRateLimit(5000, 250, clock.Now().Add(60*time.Second))
	if err := gh.UpdateCheckRun(ctx, tok, inst, "acme/payments", github.CheckRun{ID: run, Status: "in_progress"}); err != nil {
		t.Fatal(err)
	}
	listed := make(chan error, 1)
	go func() {
		_, err := gh.OpenPulls(ctx, tok, inst, "acme/payments")
		listed <- err
	}()
	waitFor(t, func() bool { return clock.pending() == 1 })
	// A check run update goes through while the listing waits.
	if err := gh.UpdateCheckRun(ctx, tok, inst, "acme/payments", github.CheckRun{ID: run, Status: "completed", Conclusion: "neutral"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-listed:
		t.Fatalf("the reconciler listing did not wait for the reset: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	clock.Advance(59 * time.Second)
	select {
	case err := <-listed:
		t.Fatalf("the listing went before the reset: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	e.GitHub.SetRateLimit(5000, 4999, clock.Now().Add(time.Hour))
	clock.Advance(time.Second)
	if err := <-listed; err != nil {
		t.Fatal(err)
	}
	if c := e.GitHub.CheckRuns(); c[0].Conclusion != "neutral" {
		t.Fatalf("check run %+v", c[0])
	}

	// Secondary limit with retry-after: the next call to the installation waits 5 s.
	e.GitHub.SetSecondaryLimit(5)
	if err := gh.UpdateCheckRun(ctx, tok, inst, "acme/payments", github.CheckRun{ID: run, Status: "in_progress"}); err == nil {
		t.Fatal("the secondary limit did not fail the call")
	}
	next := make(chan error, 1)
	go func() {
		next <- gh.UpdateCheckRun(ctx, tok, inst, "acme/payments", github.CheckRun{ID: run, Status: "in_progress"})
	}()
	waitFor(t, func() bool { return clock.pending() == 1 })
	clock.Advance(4 * time.Second)
	select {
	case err := <-next:
		t.Fatalf("the call went before retry-after passed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	clock.Advance(time.Second)
	if err := <-next; err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
