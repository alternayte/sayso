//go:build contract

package github_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/auth"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/review"
)

func TestSCN_GH_001_ManifestFlowStoresSecrets(t *testing.T) {
	e := apptest.New(t, apptest.Options{NoApp: true, SetupToken: "setup-123"})
	c := e.Client(t)
	if code := apptest.JSON(t, c, http.MethodPost, e.Base+"/api/v1/setup/session", map[string]string{"token": "setup-123"}, nil); code != http.StatusNoContent && code != http.StatusOK {
		t.Fatalf("setup session: %d", code)
	}
	var form github.ManifestForm
	if code := apptest.JSON(t, c, http.MethodPost, e.Base+"/api/v1/setup/github/manifest", map[string]string{"org": "acme"}, &form); code != http.StatusOK {
		t.Fatalf("manifest: %d", code)
	}
	if !strings.HasPrefix(form.ActionURL, e.GHServer.URL+"/organizations/acme/settings/apps/new?state=") {
		t.Fatalf("action url %q", form.ActionURL)
	}
	// The browser posts the manifest form to GitHub, which redirects to the callback.
	var final string
	c.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		final = req.URL.String()
		if strings.HasPrefix(final, e.Base) && !strings.Contains(final, "/setup/github/callback") {
			return http.ErrUseLastResponse
		}
		return nil
	}
	resp, err := c.PostForm(form.ActionURL, url.Values{"manifest": {form.Manifest}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if final != e.Base+"/signin?setup=app_created" {
		t.Fatalf("final redirect %q", final)
	}

	perms, events, hook := e.GitHub.LastManifest()
	if hook != e.Base+"/webhooks/github" || perms["checks"] != "write" || perms["contents"] != "write" || perms["members"] != "read" {
		t.Fatalf("manifest hook %q perms %v", hook, perms)
	}
	for _, ev := range []string{"pull_request", "pull_request_review_thread", "issue_comment", "check_run"} {
		if !slices.Contains(events, ev) {
			t.Fatalf("manifest events %v lack %s", events, ev)
		}
	}
	for _, name := range []string{"github.client_secret", "github.private_key", "github.webhook_secret"} {
		if n := e.Count(t, `SELECT count(*) FROM secrets WHERE name = $1 AND kind = 'stored' AND ciphertext IS NOT NULL`, name); n != 1 {
			t.Fatalf("secret %s stored %d times", name, n)
		}
	}
	if n := e.Count(t, `SELECT count(*) FROM secrets WHERE position(convert_to($1, 'UTF8') in ciphertext) > 0`, e.GitHub.WebhookSecret); n != 0 {
		t.Fatal("the webhook secret is stored in plaintext")
	}
	if n := e.Count(t, `SELECT count(*) FROM github_app WHERE app_id = 77 AND slug = 'sayso-acme' AND org_login = 'acme' AND client_id = $1`, e.GitHub.ClientID); n != 1 {
		t.Fatal("github_app row missing")
	}
	if n := e.Count(t, `SELECT count(*) FROM audit_log WHERE action = 'github.app_created'`); n != 1 {
		t.Fatal("no audit row for the App")
	}
	var st auth.SetupStateBody
	apptest.JSON(t, e.Client(t), http.MethodGet, e.Base+"/api/v1/setup/state", nil, &st)
	if !st.AppExists || st.InstallURL != e.GHServer.URL+"/apps/sayso-acme/installations/new" {
		t.Fatalf("setup state %+v", st)
	}
	// The flow works once only (SI-14).
	if code := apptest.JSON(t, c, http.MethodPost, e.Base+"/api/v1/setup/github/manifest", map[string]string{"org": "acme"}, nil); code != http.StatusNotFound {
		t.Fatalf("second manifest: %d", code)
	}
}

func TestSCN_GH_002_InstallationEventsUpdateRepositories(t *testing.T) {
	e := apptest.New(t, apptest.Options{BootstrapAdmins: []string{"octo-admin"}})
	names := func() []string {
		rows, err := e.Deps.Pool.Query(t.Context(), `SELECT full_name FROM repositories WHERE removed_at IS NULL ORDER BY full_name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			out = append(out, n)
		}
		return out
	}
	if _, code := e.GitHub.Install("acme/payments"); code != http.StatusOK {
		t.Fatalf("install: %d", code)
	}
	if got := names(); !slices.Equal(got, []string{"acme/payments"}) {
		t.Fatalf("after install: %v", got)
	}
	e.GitHub.ChangeRepositories("added", "acme/web", "acme/shared-go")
	if got := names(); !slices.Equal(got, []string{"acme/payments", "acme/shared-go", "acme/web"}) {
		t.Fatalf("after add: %v", got)
	}
	e.GitHub.ChangeRepositories("removed", "acme/web")
	if got := names(); !slices.Equal(got, []string{"acme/payments", "acme/shared-go"}) {
		t.Fatalf("after remove: %v", got)
	}
	if n := e.Count(t, `SELECT count(*) FROM installations WHERE installation_id = 4242 AND account_login = 'acme'`); n != 1 {
		t.Fatal("installation row missing")
	}
	// The repositories API lists the current set for every role.
	viewer, _ := e.SignIn(t, "dev-bob")
	var list []map[string]any
	if code := apptest.JSON(t, viewer, http.MethodGet, e.Base+"/api/v1/repositories", nil, &list); code != 200 || len(list) != 2 {
		t.Fatalf("repositories API: %d %v", code, list)
	}
	// Admins see the install link.
	admin, _ := e.SignIn(t, "octo-admin")
	var gh github.GitHubBody
	apptest.JSON(t, admin, http.MethodGet, e.Base+"/api/v1/github", nil, &gh)
	if gh.App == nil || gh.App.InstallURL != e.GHServer.URL+"/apps/sayso-acme/installations/new" || len(gh.Installations) != 1 {
		t.Fatalf("github settings: %+v", gh)
	}
}

func TestSCN_GH_003_WrongSignatureRefusedBeforeParsing(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	good, _ := json.Marshal(map[string]any{"action": "created", "installation": map[string]any{"id": 4242, "account": map[string]any{"login": "acme"}}, "repositories": []any{}})
	for name, body := range map[string][]byte{"valid body": good, "malformed body": []byte(`{"action": "created", `)} {
		code := post(t, e, "installation", "bad-"+strings.ReplaceAll(name, " ", "-"), github.Sign("not-the-secret", body), body)
		if code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d", name, code)
		}
	}
	if code := post(t, e, "installation", "no-signature", "", good); code != http.StatusUnauthorized {
		t.Fatalf("missing signature: %d", code)
	}
	if n := e.Count(t, `SELECT count(*) FROM audit_log WHERE action = 'webhook.signature_refused'`); n != 3 {
		t.Fatalf("audit rows: %d", n)
	}
	// No parser ran: nothing was recorded or changed.
	if n := e.Count(t, `SELECT count(*) FROM webhook_deliveries`) + e.Count(t, `SELECT count(*) FROM installations`); n != 0 {
		t.Fatalf("a refused delivery changed state: %d rows", n)
	}
	// A valid signature with a malformed body is parsed and refused as bad input.
	bad := []byte(`{"action": `)
	if code := post(t, e, "installation", "malformed-signed", github.Sign(apptest.WebhookSecret, bad), bad); code != http.StatusBadRequest {
		t.Fatalf("signed malformed body: %d", code)
	}
}

func TestSCN_GH_004_DeliveryHandledOnce(t *testing.T) {
	e := apptest.New(t, apptest.Options{Temporal: true})
	installAcme(t, e)
	pr := e.GitHub.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice"})
	body, _ := json.Marshal(map[string]any{
		"action": "opened", "number": pr.Number, "installation": map[string]any{"id": 4242},
		"repository":   map[string]any{"id": e.GitHub.Repo("acme/payments").ID, "full_name": "acme/payments"},
		"pull_request": map[string]any{"number": pr.Number, "user": map[string]any{"login": "dev-alice"}, "head": map[string]any{"sha": pr.HeadSHA}, "base": map[string]any{"sha": pr.BaseSHA}},
	})
	sig := github.Sign(apptest.WebhookSecret, body)
	for i := 0; i < 2; i++ {
		if code := post(t, e, "pull_request", "same-delivery-id", sig, body); code != http.StatusOK {
			t.Fatalf("delivery %d: status %d", i, code)
		}
	}
	got := signals(t, e.Temporal, wfID(e, "acme/payments", pr.Number))
	if len(got) != 1 || got[0].Name != review.SignalHeadUpdated || got[0].DeliveryID != "same-delivery-id" {
		t.Fatalf("signals: %+v", got)
	}
}

func TestSCN_GH_006_EventsMapToSignals(t *testing.T) {
	e := apptest.New(t, apptest.Options{Temporal: true})
	installAcme(t, e)
	gh := e.GitHub
	repo := gh.Repo("acme/payments")
	pr := gh.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice"})
	id := wfID(e, "acme/payments", pr.Number)
	repoJSON := map[string]any{"id": repo.ID, "full_name": repo.FullName}
	prJSON := map[string]any{"number": pr.Number, "user": map[string]any{"login": "dev-alice"}}

	gh.PullEvent("acme/payments", pr.Number, "opened", nil)
	gh.PullEvent("acme/payments", pr.Number, "reopened", nil)
	gh.PullEvent("acme/payments", pr.Number, "ready_for_review", nil)
	gh.Push("acme/payments", pr.Number, false)
	gh.Push("acme/payments", pr.Number, true)
	gh.SetPull("acme/payments", pr.Number, func(p *fakegh.Pull) { p.Draft = true })
	gh.PullEvent("acme/payments", pr.Number, "converted_to_draft", nil)
	gh.SetPull("acme/payments", pr.Number, func(p *fakegh.Pull) { p.Draft = false })
	gh.Send("issue_comment", map[string]any{"action": "created", "repository": repoJSON,
		"issue":   map[string]any{"number": pr.Number, "pull_request": map[string]any{}},
		"comment": map[string]any{"id": 501, "body": "/sayso review", "user": map[string]any{"login": "dev-alice"}}})
	gh.Send("issue_comment", map[string]any{"action": "created", "repository": repoJSON,
		"issue":   map[string]any{"number": pr.Number, "pull_request": map[string]any{}},
		"comment": map[string]any{"id": 502, "body": "Thanks, this looks fine.", "user": map[string]any{"login": "dev-alice"}}})
	finding := gh.AddComment("acme/payments", "sayso-acme[bot]", "**S-1 · major**\nBody\n<!-- sayso:finding:0190 -->", 0)
	gh.Send("pull_request_review_comment", map[string]any{"action": "created", "repository": repoJSON, "pull_request": prJSON,
		"comment": map[string]any{"id": 601, "body": "This is intended.", "in_reply_to_id": finding, "user": map[string]any{"login": "dev-alice"}}})
	gh.Send("pull_request_review_comment", map[string]any{"action": "created", "repository": repoJSON, "pull_request": prJSON,
		"comment": map[string]any{"id": 602, "body": "/sayso explain S-1", "user": map[string]any{"login": "dev-alice"}}})
	gh.Send("pull_request_review_comment", map[string]any{"action": "created", "repository": repoJSON, "pull_request": prJSON,
		"comment": map[string]any{"id": 603, "body": "Rename this variable.", "user": map[string]any{"login": "dev-alice"}}})
	gh.Send("pull_request_review_thread", map[string]any{"action": "resolved", "repository": repoJSON, "pull_request": prJSON,
		"thread": map[string]any{"node_id": "PRRT_1", "comments": []any{map[string]any{"id": finding, "body": "<!-- sayso:finding:0190 -->", "user": map[string]any{"login": "sayso-acme[bot]"}}}}})
	gh.Send("pull_request_review_thread", map[string]any{"action": "resolved", "repository": repoJSON, "pull_request": prJSON,
		"thread": map[string]any{"node_id": "PRRT_2", "comments": []any{map[string]any{"id": 603, "body": "Rename this variable.", "user": map[string]any{"login": "dev-alice"}}}}})
	gh.Send("check_run", map[string]any{"action": "rerequested", "repository": repoJSON,
		"check_run": map[string]any{"name": "sayso", "head_sha": "abc123", "app": map[string]any{"id": 77}, "pull_requests": []any{map[string]any{"number": pr.Number}}}})
	gh.Send("check_run", map[string]any{"action": "rerequested", "repository": repoJSON,
		"check_run": map[string]any{"name": "ci", "head_sha": "abc123", "app": map[string]any{"id": 12}, "pull_requests": []any{map[string]any{"number": pr.Number}}}})
	gh.SetPull("acme/payments", pr.Number, func(p *fakegh.Pull) { p.State, p.Merged = "closed", true })
	gh.PullEvent("acme/payments", pr.Number, "closed", nil)

	for _, d := range gh.Deliveries() {
		if d.StatusCode != http.StatusOK {
			t.Fatalf("delivery %s %s: status %d", d.Event, d.Action, d.StatusCode)
		}
	}
	got := signals(t, e.Temporal, id)
	want := []struct {
		name  string
		check func(review.Signal) bool
	}{
		{review.SignalHeadUpdated, func(s review.Signal) bool { return s.Trigger == review.TriggerOpened && s.HeadSHA != "" }},
		{review.SignalHeadUpdated, func(s review.Signal) bool { return s.Trigger == review.TriggerOpened }},
		{review.SignalHeadUpdated, func(s review.Signal) bool { return s.Trigger == review.TriggerReady }},
		{review.SignalHeadUpdated, func(s review.Signal) bool { return s.Trigger == review.TriggerPush && !s.Forced }},
		{review.SignalHeadUpdated, func(s review.Signal) bool { return s.Trigger == review.TriggerPush && s.Forced }},
		{review.SignalPausedDraft, func(review.Signal) bool { return true }},
		{review.SignalCommand, func(s review.Signal) bool { return s.CommentID == 501 && s.CommentSource == "issue" }},
		{review.SignalThreadReply, func(s review.Signal) bool { return s.CommentID == 601 && s.InReplyTo == finding }},
		{review.SignalCommand, func(s review.Signal) bool { return s.CommentID == 602 && s.CommentSource == "review" }},
		{review.SignalHumanComment, func(s review.Signal) bool { return s.CommentID == 603 }},
		{review.SignalThreadState, func(s review.Signal) bool { return s.ThreadNodeID == "PRRT_1" && s.Resolved }},
		{review.SignalHeadUpdated, func(s review.Signal) bool { return s.Trigger == review.TriggerCheckRerequest && s.HeadSHA == "abc123" }},
		{review.SignalClosed, func(s review.Signal) bool { return s.Merged }},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d signals, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Name != w.name || !w.check(got[i].Signal) {
			t.Fatalf("signal %d: got %s %+v, want %s", i, got[i].Name, got[i].Signal, w.name)
		}
	}

	// A draft pull request and one opened by the App start no workflow.
	draft := gh.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice", Draft: true})
	gh.PullEvent("acme/payments", draft.Number, "opened", nil)
	bot := gh.OpenPull("acme/payments", fakegh.PullOptions{Author: "sayso-acme[bot]", Head: "sayso/fix/1-2"})
	gh.PullEvent("acme/payments", bot.Number, "opened", nil)
	// A repository in mode off starts none.
	e.SQL(t, `UPDATE repositories SET mode = 'off' WHERE full_name = 'acme/web'`)
	off := gh.OpenPull("acme/web", fakegh.PullOptions{Author: "dev-alice"})
	gh.PullEvent("acme/web", off.Number, "opened", nil)
	for _, w := range []string{wfID(e, "acme/payments", draft.Number), wfID(e, "acme/payments", bot.Number), wfID(e, "acme/web", off.Number)} {
		if exists(t, e.Temporal, w) {
			t.Fatalf("workflow %s exists", w)
		}
	}
}

func TestSCN_GH_010_WebhookToSignalLatency(t *testing.T) {
	e := apptest.New(t, apptest.Options{Temporal: true})
	installAcme(t, e)
	// Warm up the Temporal connection and the token cache.
	warm := e.GitHub.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice"})
	e.GitHub.PullEvent("acme/payments", warm.Number, "opened", nil)

	const n = 200
	var durations []time.Duration
	for i := 0; i < n; i++ {
		pr := e.GitHub.OpenPull("acme/payments", fakegh.PullOptions{Author: "dev-alice"})
		start := time.Now()
		if _, code := e.GitHub.PullEvent("acme/payments", pr.Number, "opened", nil); code != http.StatusOK {
			t.Fatalf("delivery %d: %d", i, code)
		}
		durations = append(durations, time.Since(start))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[n*95/100-1]
	t.Logf("webhook to signal p95 %s over %d deliveries", p95, n)
	if p95 > 2*time.Second {
		t.Fatalf("p95 %s is more than 2 s", p95)
	}
	if got := e.Count(t, `SELECT count(*) FROM webhook_deliveries WHERE event = 'pull_request' AND handled_at IS NOT NULL`); got != n+1 {
		t.Fatalf("handled deliveries: %d", got)
	}
}
