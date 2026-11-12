package runner_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/runner"
)

func create(t *testing.T, e *apptest.Env, now time.Time) (runner.AgentRun, string) {
	t.Helper()
	r, tok, err := runner.Create(context.Background(), e.Deps.Pool, runner.NewRun{Kind: "noop", Spec: runner.Spec{Stages: []string{}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	return r, tok
}

func call(t *testing.T, e *apptest.Env, method, path, tok string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.Base+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestSCN_RUN_001_RunTokens(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	run, tok := create(t, e, time.Now())
	if code, _ := call(t, e, http.MethodGet, "/api/runner/v1/spec", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("spec without token: %d", code)
	}
	code, b := call(t, e, http.MethodGet, "/api/runner/v1/spec", tok, nil)
	var spec runner.Spec
	_ = json.Unmarshal(b, &spec)
	if code != 200 || spec.RunID != run.ID.String() || spec.Kind != "noop" {
		t.Fatalf("spec: %d %s", code, b)
	}
	// Only the hash is stored.
	if n := e.Count(t, `SELECT count(*) FROM agent_runs WHERE position(convert_to($1, 'UTF8') in token_hash) > 0 OR spec::text LIKE '%' || $1 || '%'`, tok); n != 0 {
		t.Fatal("the token is stored in plaintext")
	}
	if n := e.Count(t, `SELECT count(*) FROM agent_runs WHERE token_hash = sha256(convert_to($1, 'UTF8'))`, tok); n != 1 {
		t.Fatal("the token hash is not stored")
	}
	// After complete, the token fails on every runner and proxy route.
	result := runner.Result{RunID: run.ID.String(), Kind: "noop", Harness: runner.Harness{Name: "pi", Version: "0.73.1"}}
	if code, b := call(t, e, http.MethodPost, "/api/runner/v1/complete", tok, runner.Complete{ExitStatus: "completed", Result: &result}); code != http.StatusNoContent {
		t.Fatalf("complete: %d %s", code, b)
	}
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/runner/v1/spec"}, {http.MethodPost, "/api/runner/v1/events"}, {http.MethodPost, "/api/runner/v1/heartbeat"},
		{http.MethodPost, "/api/runner/v1/complete"}, {http.MethodPut, "/api/runner/v1/artifacts/result.json"},
		{http.MethodPost, "/llm/any/chat/completions"}, {http.MethodPost, "/mcp/any"},
	}
	for _, r := range routes {
		if code, _ := call(t, e, r.method, r.path, tok, map[string]any{}); code != http.StatusUnauthorized {
			t.Fatalf("%s %s after complete: %d", r.method, r.path, code)
		}
	}
	// A token expires after 2 hours.
	_, old := create(t, e, time.Now().Add(-2*time.Hour-time.Minute))
	for _, r := range routes {
		if code, _ := call(t, e, r.method, r.path, old, map[string]any{}); code != http.StatusUnauthorized {
			t.Fatalf("%s %s after 2 hours: %d", r.method, r.path, code)
		}
	}
	_, fresh := create(t, e, time.Now().Add(-time.Hour))
	if code, _ := call(t, e, http.MethodPost, "/api/runner/v1/heartbeat", fresh, nil); code != 200 {
		t.Fatalf("heartbeat of a 1-hour-old run: %d", code)
	}
}

func payload(v any) string {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_ = json.NewEncoder(w).Encode(v)
	_ = w.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestSCN_RUN_008_EventsAndResultSchema(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	run, tok := create(t, e, time.Now())
	// A result that fails the schema is refused with the reason.
	bad := map[string]any{"exit_status": "completed", "result": map[string]any{"run_id": run.ID.String(), "kind": "noop", "harness": map[string]any{"name": "pi", "version": "1"},
		"usage": map[string]any{"tokens_in": 0, "tokens_out": 0}, "findings": []any{map[string]any{"severity": "fatal"}}}}
	code, b := call(t, e, http.MethodPost, "/api/runner/v1/complete", tok, bad)
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(b), "severity") {
		t.Fatalf("bad result: %d %s", code, b)
	}
	// The secret values in previews and payloads are redacted.
	ev := runner.Event{Seq: 1, At: time.Now().UTC().Format(time.RFC3339Nano), Type: "tool_call", Name: "bash", Preview: "token " + tok,
		Payload: payload(map[string]string{"out": "ghs_abcdefghijklmnopqrstuvwx " + tok})}
	if code, b := call(t, e, http.MethodPost, "/api/runner/v1/events", tok, runner.EventsBody{Events: []runner.Event{ev}}); code != http.StatusNoContent {
		t.Fatalf("events: %d %s", code, b)
	}
	var preview string
	_ = e.Deps.Pool.QueryRow(context.Background(), `SELECT preview FROM run_events WHERE run_id = $1 AND seq = 1`, run.ID).Scan(&preview)
	if strings.Contains(preview, tok) || !strings.Contains(preview, "[redacted:run_token]") {
		t.Fatalf("preview %q", preview)
	}
	var data []byte
	_ = e.Deps.Pool.QueryRow(context.Background(), `SELECT data FROM blobs WHERE key = $1`, fmt.Sprintf("runs/%s/events/1.json.gz", run.ID)).Scan(&data)
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if strings.Contains(string(plain), tok) || strings.Contains(string(plain), "ghs_abc") || !strings.Contains(string(plain), "[redacted:github_token]") {
		t.Fatalf("payload %s", plain)
	}

	// 500 events per second for 60 s are stored without loss (NFR-006).
	const perSecond, seconds, batch = 500, 60, 50
	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan string, perSecond*seconds/batch)
	seq := int64(100)
	for s := 0; s < seconds; s++ {
		tick := start.Add(time.Duration(s) * time.Second)
		time.Sleep(time.Until(tick))
		for k := 0; k < perSecond/batch; k++ {
			evs := make([]runner.Event, batch)
			for i := range evs {
				seq++
				evs[i] = runner.Event{Seq: seq, At: time.Now().UTC().Format(time.RFC3339Nano), Type: "tool_call", Name: "read", Preview: "read file",
					Payload: payload(map[string]any{"seq": seq, "out": strings.Repeat("x", 400)})}
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if code, b := call(t, e, http.MethodPost, "/api/runner/v1/events", tok, runner.EventsBody{Events: evs}); code != http.StatusNoContent {
					errs <- fmt.Sprintf("%d %s", code, b)
				}
			}()
		}
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(errs)
	for e := range errs {
		t.Fatalf("batch refused: %s", e)
	}
	if n := e.Count(t, `SELECT count(*) FROM run_events WHERE run_id = $1 AND seq > 100 AND payload_key IS NOT NULL`, run.ID); n != perSecond*seconds {
		t.Fatalf("stored %d of %d events", n, perSecond*seconds)
	}
	if elapsed > time.Duration(seconds+5)*time.Second {
		t.Fatalf("ingestion fell behind: %s for %d s of events", elapsed, seconds)
	}
	// A retried batch is stored once.
	call(t, e, http.MethodPost, "/api/runner/v1/events", tok, runner.EventsBody{Events: []runner.Event{ev}})
	if n := e.Count(t, `SELECT count(*) FROM run_events WHERE run_id = $1 AND seq = 1`, run.ID); n != 1 {
		t.Fatalf("seq 1 stored %d times", n)
	}
}

func TestSCN_RUN_002_ProcessRunnerEnvironmentAndCancel(t *testing.T) {
	parent := t.TempDir()
	out := filepath.Join(t.TempDir(), "env.txt")
	vars := map[string]string{"PATH": "/usr/bin:/bin", "HTTPS_PROXY": "http://proxy.acme.test:3128", "SAYSO_SECRET_KEY": "must-not-pass", "AWS_SECRET_ACCESS_KEY": "nope", "GITHUB_TOKEN": "nope"}
	p := &runner.Process{
		// The stage script prints its environment, then starts a child and waits.
		Command: []string{"/bin/sh", "-c", "env > " + out + "; sleep 300 & sleep 300"},
		Dir:     parent,
		Getenv:  func(k string) string { return vars[k] },
	}
	h, err := p.Start(context.Background(), runner.Job{RunID: "r1", Token: "srt_token", APIURL: "http://api.acme.test"})
	if err != nil {
		t.Fatal(err)
	}
	var env []byte
	deadline := time.Now().Add(5 * time.Second)
	for len(env) == 0 && time.Now().Before(deadline) {
		env, _ = os.ReadFile(out)
		time.Sleep(20 * time.Millisecond)
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(string(env)), "\n") {
		k, _, _ := strings.Cut(l, "=")
		if k != "PWD" && k != "SHLVL" && k != "_" && k != "OLDPWD" {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	want := []string{"HOME", "HTTPS_PROXY", "PATH", "SAYSO_API_URL", "SAYSO_RUN_ID", "SAYSO_RUN_TOKEN"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("environment %v, want %v", names, want)
	}
	if !strings.Contains(string(env), "HOME="+parent) {
		t.Fatalf("HOME is not inside the run directory: %s", env)
	}
	dirs, _ := os.ReadDir(parent)
	if len(dirs) != 1 {
		t.Fatalf("run directories: %d", len(dirs))
	}
	if err := h.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reason, _ := h.Wait(context.Background()); reason != runner.ExitCancelled {
		t.Fatalf("exit reason %s", reason)
	}
	if dirs, _ := os.ReadDir(parent); len(dirs) != 0 {
		t.Fatal("the run directory was not removed")
	}
	// No process of the group is left.
	pid := strings.TrimPrefix(h.Ref(), "pid:")
	if b, _ := os.ReadFile("/proc/" + pid + "/status"); len(b) > 0 {
		t.Fatal("the agent process still runs")
	}
	if out, _ := run("pgrep", "-g", pid); strings.TrimSpace(out) != "" {
		t.Fatalf("processes of the group still run: %s", out)
	}
}

func run(name string, args ...string) (string, error) {
	out, err := osexec.Command(name, args...).Output()
	return string(out), err
}
