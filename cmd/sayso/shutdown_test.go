package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/alternayte/sayso/internal/app"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/secrets"
	"github.com/alternayte/sayso/internal/testenv"
)

func TestSCN_CORE_008_GracefulShutdown(t *testing.T) {
	dbURL := testenv.Postgres(t)
	tp := testenv.TemporalDev(t)

	// A fake GitHub that answers the reconciler listing and the compare call slowly.
	fake := fakegh.New()
	var listingDone, compareStarted, listingStarted atomic.Bool
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/hook/deliveries":
			listingStarted.Store(true)
			time.Sleep(4 * time.Second)
			fake.Handler().ServeHTTP(w, r)
			listingDone.Store(true)
			return
		case strings.Contains(r.URL.Path, "/compare/"):
			compareStarted.Store(true)
			time.Sleep(4 * time.Second)
		}
		fake.Handler().ServeHTTP(w, r)
	}))
	defer slow.Close()

	s := startServe(t, dbURL, tp.Address, "all")
	s.waitStatus(t, "/readyz", http.StatusOK, 60*time.Second)

	// Seed the App, the GitHub URLs and one repository in comment mode.
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sec, err := secrets.Open(ctx, pool, mustKey(t), app.NewRedactor(), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	a := &github.App{AppID: 77, Slug: "sayso-acme", Org: "acme", ClientID: fake.ClientID, ClientSecret: fake.Secret, PrivateKeyPEM: pemKey}
	if err := github.SaveApp(ctx, pool, sec, a, "whsec", slow.URL+"/apps/sayso-acme"); err != nil {
		t.Fatal(err)
	}
	url, _ := json.Marshal(slow.URL)
	for _, q := range []string{
		`UPDATE settings SET value = $1 WHERE key IN ('github_web_url', 'github_api_url')`,
	} {
		if _, err := pool.Exec(ctx, q, url); err != nil {
			t.Fatal(err)
		}
	}
	repo := fake.Repo("acme/payments")
	if _, err := pool.Exec(ctx, `INSERT INTO installations (installation_id, account_login) VALUES (4242, 'acme')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO repositories (id, github_id, installation_id, full_name, mode) VALUES (gen_random_uuid(), $1, 4242, 'acme/payments', 'comment')`, repo.ID); err != nil {
		t.Fatal(err)
	}

	// A running activity: the scheduled reconciler waits on the slow listing.
	tc, err := client.Dial(client.Options{HostPort: tp.Address})
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	waitTrue(t, func() bool {
		_, err := tc.ScheduleClient().GetHandle(ctx, github.ReconcilerSchedule).Describe(ctx)
		return err == nil
	}, 20*time.Second)
	if err := tc.ScheduleClient().GetHandle(ctx, github.ReconcilerSchedule).Trigger(ctx, client.ScheduleTriggerOptions{}); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, listingStarted.Load, 20*time.Second)
	desc, err := tc.ScheduleClient().GetHandle(ctx, github.ReconcilerSchedule).Describe(ctx)
	if err != nil || len(desc.Info.RunningWorkflows) != 1 {
		t.Fatalf("reconciler run: %v %+v", err, desc)
	}
	runID := desc.Info.RunningWorkflows[0].WorkflowID

	// A request in progress: a push event waits on the slow compare call.
	pr := fake.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice"})
	body, _ := json.Marshal(map[string]any{
		"action": "synchronize", "before": pr.BaseSHA, "after": pr.HeadSHA, "installation": map[string]any{"id": 4242},
		"repository":   map[string]any{"id": repo.ID, "full_name": repo.FullName},
		"pull_request": map[string]any{"number": pr.Number, "user": map[string]any{"login": "dev-alice"}, "head": map[string]any{"sha": pr.HeadSHA}, "base": map[string]any{"sha": pr.BaseSHA}},
	})
	status := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, s.base+"/webhooks/github", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", "shutdown-1")
		req.Header.Set("X-Hub-Signature-256", github.Sign("whsec", body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- 0
			return
		}
		resp.Body.Close()
		status <- resp.StatusCode
	}()
	waitTrue(t, compareStarted.Load, 10*time.Second)

	start := time.Now()
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- s.cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("serve exited with %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not exit within 30 s")
	}
	t.Logf("exit after %s", time.Since(start))
	if code := <-status; code != http.StatusOK {
		t.Fatalf("the request in progress got %d", code)
	}
	if !listingDone.Load() {
		t.Fatal("the running activity did not finish its call")
	}
	// The activity completed before the worker stopped.
	completed := false
	it := tc.GetWorkflowHistory(ctx, runID, "", false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for it.HasNext() {
		ev, err := it.Next()
		if err != nil {
			break
		}
		if ev.GetEventType() == enums.EVENT_TYPE_ACTIVITY_TASK_COMPLETED {
			completed = true
			break
		}
	}
	if !completed {
		t.Fatal("the reconciler activity did not complete")
	}
	if !strings.Contains(s.logs.String(), "Stopped Worker") {
		t.Fatal("the worker did not stop polling")
	}
}

func mustKey(t *testing.T) []byte {
	t.Helper()
	cfg, _, err := app.LoadConfig(func(k string) string {
		if k == "SAYSO_SECRET_KEY" {
			return testenv.SecretKey()
		}
		return ""
	}, nil, app.Need{SecretKey: true})
	if err != nil {
		t.Fatal(err)
	}
	return cfg.SecretKey
}

func waitTrue(t *testing.T, cond func() bool, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
