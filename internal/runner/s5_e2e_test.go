//go:build e2e

package runner_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/alternayte/sayso/internal/audit"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/repos"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/skills"
	"github.com/alternayte/sayso/internal/skills/skaeltest"
)

func bashOut(h *harness, t *testing.T, o outcome) string {
	t.Helper()
	for _, e := range h.events(t, o.run.ID) {
		if e.Type == "tool_call" && e.Name == "bash" {
			return e.Preview
		}
	}
	t.Fatalf("no bash output; logs:\n%s", o.logs)
	return ""
}

func TestSCN_PKG_003_NpmThroughTheProxy(t *testing.T) {
	h := newHarness(t)
	o := h.start(t, runOpts{repo: "acme/web", script: "[script:review-feeds]", execution: true, feeds: []string{"acme-npm"},
		overlay: runner.Overlay{Block: true, Toolchain: "auto", Restore: "auto"}})
	mustStatus(t, o, "done")
	ok := false
	for _, e := range h.events(t, o.run.ID) {
		if e.Type == "env" && e.Name == "npm ci" {
			ok = e.Status == "ok"
		}
	}
	if !ok {
		for _, e := range h.events(t, o.run.ID) {
			t.Logf("event %s %s %s %.600s", e.Type, e.Name, e.Status, e.Preview)
		}
		t.Fatalf("npm ci did not pass through the proxy; logs:\n%s", o.logs)
	}
	out := bashOut(h, t, o)
	if !strings.Contains(out, "STATUS=0") || !strings.Contains(out, "MONEY=yes") || !strings.Contains(out, "CRED=0") {
		t.Fatalf("run checks: %s", out)
	}
	if h.feeds.Count(".tgz") != 1 {
		t.Fatalf("the feed served %d tarballs", h.feeds.Count(".tgz"))
	}
	for _, r := range h.feeds.Requests() {
		if !strings.HasPrefix(r.Auth, "Basic ") || strings.Contains(r.Auth, "srt_") {
			t.Fatalf("the feed got %q", r.Auth)
		}
	}
	if n := h.e.Count(t, `SELECT count(*) FROM run_events WHERE run_id = $1 AND type = 'pkg_summary' AND preview LIKE 'acme-npm:%'`, o.run.ID); n != 1 {
		t.Fatalf("pkg_summary events: %d", n)
	}
}

func TestSCN_PKG_004_InjectModeWritesTheCredential(t *testing.T) {
	h := newHarness(t)
	h.e.SQL(t, `UPDATE package_feeds SET mode = 'inject', inject_confirmed_by = 'octo-admin' WHERE name = 'acme-npm'`)
	o := h.start(t, runOpts{repo: "acme/web", script: "[script:review-npmrc]", execution: true, feeds: []string{"acme-npm"},
		overlay: runner.Overlay{Block: true, Toolchain: "none", Restore: "none"}})
	mustStatus(t, o, "done")
	// The credential is in ~/.npmrc; stored text shows it redacted.
	if out := bashOut(h, t, o); !strings.Contains(out, "[redacted:feed.acme-npm.secret]") || strings.Contains(out, feedSecret) || strings.Contains(out, "srt_") {
		t.Fatalf("~/.npmrc: %s", out)
	}
}

func TestSCN_PKG_005_PrivateGoModuleThroughTheGitProxy(t *testing.T) {
	h := newHarness(t)
	sha := h.e.GitHub.MainSHA("acme/shared-go")
	mod := fmt.Sprintf("module github.com/acme/payments\n\ngo 1.26\n\nrequire github.com/acme/shared-go v0.0.0-20260105090000-%s\n", sha[:12])
	o := h.start(t, runOpts{script: "[script:review-gomod]", execution: true, feeds: []string{"acme-git"},
		pull:    fakegh.PullOptions{Files: map[string]string{"go.mod": mod}},
		overlay: runner.Overlay{Block: true, Toolchain: "auto", Restore: "auto"}})
	mustStatus(t, o, "done")
	status := ""
	for _, e := range h.events(t, o.run.ID) {
		if e.Type == "env" && e.Name == "go mod download" {
			status = e.Status + " " + e.Preview
		}
	}
	if !strings.HasPrefix(status, "ok") {
		for _, e := range h.events(t, o.run.ID) {
			t.Logf("event %s %s %s %.900s", e.Type, e.Name, e.Status, e.Preview)
		}
		t.Fatalf("go mod download: %s", status)
	}
	// A repository that the feed does not list gets 403 from the git proxy.
	if out := bashOut(h, t, o); !strings.Contains(out, "shared-go@") || !strings.Contains(out, "DENIED=1") {
		t.Fatalf("module cache and unlisted repository: %s", out)
	}
}

// resolver returns the skill of acme/payments at its main head.
func resolve(t *testing.T, h *harness) (runner.Skill, *runner.Skael) {
	t.Helper()
	ctx := context.Background()
	app, err := github.LoadApp(ctx, h.e.Deps.Pool, h.e.F.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	gh, err := h.e.F.GitHubClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := gh.InstallationToken(ctx, app, h.e.GitHub.InstallID, github.TokenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	read := func(ctx context.Context, path, ref string) ([]byte, bool, error) {
		b, err := gh.FileAt(ctx, tok, h.e.GitHub.InstallID, "acme/payments", path, ref)
		if err == github.ErrNotFound {
			return nil, false, nil
		}
		return b, err == nil, err
	}
	sk, skael, err := h.e.F.Skills.Resolve(ctx, read, repos.DefaultSkillPath, h.e.GitHub.MainSHA("acme/payments"), "")
	if err != nil {
		t.Fatal(err)
	}
	return sk, skael
}

func skaelSettings(t *testing.T, h *harness, sk *skaeltest.Skael, url string) {
	t.Helper()
	ctx := context.Background()
	for k, v := range map[string]any{"skael_url": url, "skael_default_set": skaeltest.Set} {
		raw, _ := json.Marshal(v)
		if _, err := h.e.F.Settings.Set(ctx, audit.System, k, raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.e.F.Secrets.Put(ctx, audit.System, skills.AgentKeySecret, sk.AgentKey); err != nil {
		t.Fatal(err)
	}
}

func TestSCN_SKL_001_SkillSources(t *testing.T) {
	sk := skaeltest.Start(t)
	// Neither: the built-in skill.
	h := newHarness(t)
	skill, skael := resolve(t, h)
	o := h.start(t, runOpts{script: "[script:review-assess-only]", skill: &skill, skael: skael})
	if r := h.result(t, o.run.ID); r.SkillRef.Source != "builtin" || r.SkillRef.SHA256 == "" {
		t.Fatalf("builtin skill_ref %+v", r.SkillRef)
	}
	// A Skael set and no repository skill.
	skaelSettings(t, h, sk, "http://"+containerHost+":"+sk.Port)
	skill, skael = resolve(t, h)
	o = h.start(t, runOpts{script: "[script:review-assess-only]", skill: &skill, skael: skael})
	r := h.result(t, o.run.ID)
	want := sha256.Sum256([]byte("SKILL.md\x00" + skaeltest.SkillText + "\x00"))
	if r.SkillRef.Source != "skael" || r.SkillRef.Version != "1" || r.SkillRef.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("skael skill_ref %+v; logs:\n%s", r.SkillRef, o.logs)
	}
	// A repository skill on the base commit wins.
	text := "---\nname: pr-review\n---\n\n# acme/payments review rules\n"
	h.e.GitHub.CommitMain("acme/payments", map[string]string{repos.DefaultSkillPath: text})
	skill, skael = resolve(t, h)
	o = h.start(t, runOpts{script: "[script:review-assess-only]", skill: &skill, skael: skael})
	r = h.result(t, o.run.ID)
	want = sha256.Sum256([]byte("SKILL.md\x00" + text + "\x00"))
	if r.SkillRef.Source != "repository" || r.SkillRef.Version != o.pull.BaseSHA || r.SkillRef.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("repository skill_ref %+v", r.SkillRef)
	}
}

func TestSCN_SKL_002_SkaelPullAndHook(t *testing.T) {
	sk := skaeltest.Start(t)
	h := newHarness(t)
	skaelSettings(t, h, sk, "http://"+containerHost+":"+sk.Port)
	activations := func() string {
		return sk.Query(t, `SELECT count(*) FROM skill_events WHERE skill_name = 'pr-review' AND role = 'reviewer'`)
	}
	reported := func(o outcome) bool {
		for _, e := range h.events(t, o.run.ID) {
			if e.Type == "skill_load" && e.Status == "activation_reported" {
				return true
			}
		}
		return false
	}
	skill, skael := resolve(t, h)
	o := h.start(t, runOpts{script: "[script:review-skael]", skill: &skill, skael: skael})
	mustStatus(t, o, "done")
	var data []byte
	if err := h.e.Deps.Pool.QueryRow(context.Background(), `SELECT b.data FROM artifacts a JOIN blobs b ON b.key = a.key WHERE a.run_id = $1 AND a.name = 'manifest.json'`, o.run.ID).Scan(&data); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	var m runner.SkaelManifest
	_ = json.Unmarshal(data, &m)
	if m.Set != skaeltest.Set || len(m.Skills) != 1 || m.Skills[0].Version != 1 || m.ResolutionID == "" {
		t.Fatalf("manifest %+v", m)
	}
	// Hook on: Skael records the read during the run, under the round ID.
	if !reported(o) || activations() != "1" {
		t.Fatalf("hook on: event %v, Skael activations %s; logs:\n%s", reported(o), activations(), o.logs)
	}
	if n := sk.Query(t, `SELECT count(*) FROM runs WHERE external_run_id = '`+o.round.String()+`'`); n != "1" {
		t.Fatalf("Skael runs for the round: %s", n)
	}
	if r := h.result(t, o.run.ID); len(r.SkillsLoaded) != 1 || !r.SkillsLoaded[0].Reported {
		t.Fatalf("skills_loaded %+v", r.SkillsLoaded)
	}
	// Hook off: the run reports nothing; the API reports with the outcome.
	h.e.SQL(t, `UPDATE settings SET value = 'false' WHERE key = 'skael_activation_hook'`)
	skill, skael = resolve(t, h)
	o = h.start(t, runOpts{script: "[script:review-skael]", skill: &skill, skael: skael})
	mustStatus(t, o, "done")
	if reported(o) || activations() != "1" {
		t.Fatalf("hook off: event %v, Skael activations %s", reported(o), activations())
	}
}

func TestSCN_SKL_003_SkaelDownFallsBack(t *testing.T) {
	sk := skaeltest.Start(t)
	h := newHarness(t)
	skaelSettings(t, h, sk, "http://"+containerHost+":"+sk.Port)
	skill, skael := resolve(t, h)
	sk.Stop(t)
	o := h.start(t, runOpts{script: "[script:review-assess-only]", skill: &skill, skael: skael})
	mustStatus(t, o, "done")
	if r := h.result(t, o.run.ID); r.SkillRef.Source != "builtin" {
		t.Fatalf("skill_ref %+v", r.SkillRef)
	}
	found := false
	for _, e := range h.events(t, o.run.ID) {
		if e.Type == "error" && e.Name == "skael pull" && strings.Contains(e.Preview, "next skill source") {
			found = true
		}
	}
	if !found {
		t.Fatal("no error event names the failed pull")
	}
}
