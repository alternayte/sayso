// Package round1 holds the failing tests of review round 1 (§27). Each test
// names the finding it pins.
package round1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/config"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/publish"
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
`

// flow starts the API and the workers with a scripted runner: a round
// reports one finding on ledger/ledger.go, a fix run returns patch.
func flow(t *testing.T, patch *runner.Patch, extra func(e *apptest.Env)) (*apptest.Env, *fakegh.Pull) {
	t.Helper()
	ctx := context.Background()
	e := apptest.New(t, apptest.Options{Temporal: true})
	if _, code := e.GitHub.Install("acme/payments"); code != http.StatusOK {
		t.Fatalf("install: %d", code)
	}
	e.SQL(t, `UPDATE repositories SET mode = 'gate'`)
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
			return runnertest.Answer{Result: &runner.Result{Patch: patch}}
		}
		return runnertest.Answer{Result: &runner.Result{
			Assessment: &runner.Assessment{TLDR: "Small change.", Risk: "low", RiskReasons: []string{"small"}, WhereToLook: []runner.WhereToLook{}, Impact: []runner.Impact{}},
			Findings: []runner.Finding{{CandidateID: "c1", Severity: "major", Category: "correctness", Title: "Negative amounts pass", Body: "Post accepts a negative amount.",
				Path: ledgerFile, StartLine: 4, EndLine: 4, Evidence: runner.Evidence{Kind: "reasoning", Detail: "No check before the add."}}},
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

func fixFails(t *testing.T, e *apptest.Env, pr *fakegh.Pull) string {
	t.Helper()
	if _, code := e.GitHub.AddIssueComment("acme/payments", pr.Number, "dev-alice", "/sayso fix S-1"); code != http.StatusOK {
		t.Fatalf("comment: %d", code)
	}
	var reason string
	waitFor(t, 2*time.Minute, "the fix work to end", func() bool {
		var status string
		err := e.Deps.Pool.QueryRow(context.Background(), `SELECT status, reason FROM agent_work WHERE kind = 'fix'`).Scan(&status, &reason)
		return err == nil && (status == "failed" || status == "done")
	})
	return reason
}

// Finding 4 (SI-07): Sayso committed to a fix branch that a user pushed first.
func TestReview1_04_FixRefusesABranchSaysoDidNotCreate(t *testing.T) {
	patch := &runner.Patch{FindingID: "S-1", Title: "Refuse negative amounts", Description: "Adds a check.",
		Diff: "--- a/" + ledgerFile + "\n+++ b/" + ledgerFile + "\n@@ -4 +4 @@\n-func Post(balance, amount int64) int64 { return balance + amount }\n+func Post(balance, amount int64) int64 { return balance + max(amount, 0) }\n"}
	e, pr := flow(t, patch, nil)
	branch := "sayso/fix/" + strconv.Itoa(pr.Number) + "-1"
	if err := e.GitHub.UserBranch("acme/payments", branch, map[string]string{"evil.txt": "user commit\n"}); err != nil {
		t.Fatal(err)
	}
	writes := e.GitHub.ContentWrites("acme/payments")
	reason := fixFails(t, e, pr)
	if !strings.Contains(reason, "did not create") {
		t.Fatalf("the fix work did not fail on the user branch: %q", reason)
	}
	if n := e.GitHub.ContentWrites("acme/payments"); n != writes {
		t.Fatalf("Sayso wrote %d files to a branch it did not create", n-writes)
	}
	for _, p := range e.GitHub.Pulls("acme/payments") {
		if p.HeadRef == branch {
			t.Fatalf("Sayso opened #%d from a branch it did not create", p.Number)
		}
	}
}

// Finding 5 (SI-07): a patch path with ".." reached other contents endpoints.
func TestReview1_05_FixRefusesAPathOutsideTheRepository(t *testing.T) {
	patch := &runner.Patch{FindingID: "S-1", Title: "Fix", Description: "Fix.",
		Diff: "diff --git a/../x b/../x\nnew file mode 100644\n--- /dev/null\n+++ b/../x\n@@ -0,0 +1 @@\n+x\n"}
	e, pr := flow(t, patch, nil)
	writes := e.GitHub.ContentWrites("acme/payments")
	reason := fixFails(t, e, pr)
	if reason == "" {
		t.Fatal("the fix work did not fail")
	}
	if n := e.GitHub.ContentWrites("acme/payments"); n != writes {
		t.Fatalf("Sayso sent %d contents writes for a path outside the repository", n-writes)
	}
}

// Finding 8 (SI-05): extension secrets were stored in agent_runs.spec.
func TestReview1_08_StoredRunSpecHasNoExtensionSecret(t *testing.T) {
	const value = "acme-extension-secret-7f2c91"
	e, _ := flow(t, nil, func(e *apptest.Env) {
		if _, err := e.F.Secrets.Put(context.Background(), audit.System, "extension.acme-lint.lint_token", value); err != nil {
			t.Fatal(err)
		}
		e.SQL(t, `INSERT INTO extensions (name, spec, env, secret_ids, enabled, repositories) VALUES ('acme-lint', 'npm:@acme/pi-lint@1.2.0', '{}',
			jsonb_build_object('LINT_TOKEN', (SELECT id::text FROM secrets WHERE name = 'extension.acme-lint.lint_token')), true, '{acme/payments}')`)
	})
	var spec string
	if err := e.Deps.Pool.QueryRow(context.Background(), `SELECT spec::text FROM agent_runs ORDER BY created_at LIMIT 1`).Scan(&spec); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec, "acme-lint") {
		t.Fatalf("the round had no extension: %s", spec)
	}
	if strings.Contains(spec, value) {
		t.Fatal("agent_runs.spec holds the extension secret")
	}
}

func run(t *testing.T, e *apptest.Env, spec runner.Spec) (runner.AgentRun, string) {
	t.Helper()
	r, tok, err := runner.Create(context.Background(), e.Deps.Pool, runner.NewRun{Kind: "noop", Spec: spec}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return r, tok
}

// Finding 2 (SI-05): a run chose the content type of its artifact, and the
// API served it as a page of the Sayso origin.
func TestReview1_02_ArtifactIsNeverAPage(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	r, tok := run(t, e, runner.Spec{Kind: "noop"})
	put := func(name, ct, body string) int {
		req, _ := http.NewRequest(http.MethodPut, e.Base+"/api/runner/v1/artifacts/"+name, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", ct)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := put("tests/x.html", "text/html", "<script>fetch('/api/v1/settings')</script>"); code != http.StatusNoContent {
		t.Fatalf("put: %d", code)
	}
	if code := put("session.jsonl.gz", "application/gzip", "not gzip"); code != http.StatusUnprocessableEntity {
		t.Fatalf("a .gz artifact that is not gzip: %d", code)
	}
	c, _ := e.SignIn(t, "dev-bob")
	resp, err := c.Get(e.Base + "/api/v1/runs/" + r.ID.String() + "/artifacts/tests%2Fx.html")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "html") || strings.Contains(ct, "javascript") {
		t.Fatalf("content type %q", ct)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers %v", resp.Header)
	}
}

// mentionRe is a mention that notifies: @ and a login character, with no
// word joiner between.
var mentionRe = regexp.MustCompile(`@[A-Za-z0-9]`)

// Findings 6 and 11 (SI-16): a stray backtick, a second @ or a line break
// in a code span made a mention that notifies.
func TestReview1_06_11_NoAgentTextNotifies(t *testing.T) {
	for _, in := range []string{"x`@octocat", "@a@b", "see `code` and `@dev-alice", "a\n\n@dev-alice"} {
		if out := publish.Safe(in); mentionRe.MatchString(out) {
			t.Errorf("Safe(%q) = %q notifies", in, out)
		}
		if out := publish.Code(in); mentionRe.MatchString(out) || strings.Contains(out, "\n") || strings.Count(out, "`") != 2 {
			t.Errorf("Code(%q) = %q", in, out)
		}
	}
}

// Finding 7 (SI-01): config apply gave an extension the App private key or
// a variable of Sayso.
func TestReview1_07_ConfigApplyCannotBorrowASecret(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	svc := &config.Service{Pool: e.Deps.Pool}
	base, err := svc.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{`{secret: github.private_key}`, `{env: SAYSO_SECRET_KEY}`} {
		doc := strings.Replace(string(base), "extensions: []", "extensions:\n    - name: grab\n      kind: pi_package\n      spec: npm:@acme/grab@1.0.0\n      env: {}\n      secrets: {TOKEN: "+ref+"}\n      enabled: true\n      repositories: []", 1)
		if doc == string(base) {
			t.Fatal("the export has no empty extensions list")
		}
		if _, err := svc.Apply(context.Background(), audit.System, []byte(doc), nil); err == nil {
			t.Errorf("apply took %s for an extension secret", ref)
		}
	}
	if n := e.Count(t, `SELECT count(*) FROM extensions WHERE name = 'grab'`); n != 0 {
		t.Fatal("the extension was stored")
	}
}

// Finding 9 (SI-17): the feed proxy resolved dot segments to other paths of
// the feed host, with the feed credential.
func TestReview1_09_FeedProxyRefusesDotSegments(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(up.Close)
	e := apptest.New(t, apptest.Options{})
	e.SQL(t, `INSERT INTO package_feeds (name, kind, upstream_url) VALUES ('acme-npm', 'npm', $1)`, up.URL+"/npm/")
	_, tok := run(t, e, runner.Spec{Kind: "noop", Feeds: []runner.SpecFeed{{Name: "acme-npm"}}})
	get := func(path string) int {
		req, _ := http.NewRequest(http.MethodGet, e.Base+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("/pkg/acme-npm/left-pad"); code != http.StatusOK {
		t.Fatalf("a plain path: %d", code)
	}
	before := hits.Load()
	for _, p := range []string{"/pkg/acme-npm/../../api/security/users", "/pkg/acme-npm/%2e%2e/%2e%2e/api/security/users"} {
		if code := get(p); code != http.StatusBadRequest {
			t.Errorf("%s: %d", p, code)
		}
	}
	if hits.Load() != before {
		t.Fatal("the upstream got a request with a dot segment")
	}
}

// Finding 10 (SI-01): credential headers followed a redirect to another host.
func TestReview1_10_RedirectDropsCredentialHeaders(t *testing.T) {
	var mu sync.Mutex
	var got http.Header
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
	}))
	t.Cleanup(other.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/x", http.StatusFound)
	}))
	t.Cleanup(first.Close)
	hc, err := app.HTTPClient(&app.Config{})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, first.URL, nil)
	for _, h := range []string{"Authorization", "X-Api-Key", "Api-Key", "X-Feed-Token"} {
		req.Header.Set(h, "secret-"+h)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	for k, v := range got {
		if strings.Contains(strings.Join(v, ","), "secret-") {
			t.Errorf("the other host got %s", k)
		}
	}
}

// Finding 12: one failed status call ended a Kubernetes run that went on.
func TestReview1_12_KubeRunSurvivesAFailedStatusCall(t *testing.T) {
	var gets atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs"):
			var job map[string]any
			_ = json.NewDecoder(r.Body).Decode(&job)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(job)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/"):
			if gets.Add(1) == 1 {
				http.Error(w, `{"message":"etcd leader changed"}`, http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, `{"status":{"conditions":[{"type":"Complete","status":"True"}]}}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pods"):
			_, _ = io.WriteString(w, `{"items":[]}`)
		default:
			http.Error(w, fmt.Sprintf("unexpected %s %s", r.Method, r.URL.Path), http.StatusNotFound)
		}
	}))
	t.Cleanup(api.Close)
	k := &runner.Kubernetes{Client: runner.NewKubeClient(api.URL, api.Client()), Namespace: "sayso", Image: "sayso-agent:test", Poll: 10 * time.Millisecond}
	h, err := k.Start(context.Background(), runner.Job{RunID: "0193", Token: "srt_x", APIURL: "http://api", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	reason, err := h.Wait(context.Background())
	if err != nil || reason != runner.ExitCompleted {
		t.Fatalf("Wait = %q, %v; want completed", reason, err)
	}
}
