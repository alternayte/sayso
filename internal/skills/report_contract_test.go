//go:build contract

package skills_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/skills"
	"github.com/alternayte/sayso/internal/skills/skaeltest"
)

type outcomes struct {
	Consumers []struct {
		Consumer string `json:"consumer"`
		Versions []struct {
			Version     int      `json:"version"`
			Succeeded   int      `json:"succeeded"`
			MeanRepairs *float64 `json:"mean_repairs"`
		} `json:"versions"`
	} `json:"consumers"`
}

func TestSCN_SKL_004_ReportsToSkael(t *testing.T) {
	sk := skaeltest.Start(t)
	e := apptest.New(t, apptest.Options{})
	ctx := context.Background()
	raw, _ := json.Marshal(sk.URL)
	if _, err := e.F.Settings.Set(ctx, audit.System, "skael_url", raw); err != nil {
		t.Fatal(err)
	}
	if _, err := e.F.Settings.Set(ctx, audit.System, "skael_default_set", json.RawMessage(`"`+skaeltest.Set+`"`)); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{skills.AgentKeySecret: sk.AgentKey, skills.ReporterKeySecret: sk.ReporterKey} {
		if _, err := e.F.Secrets.Put(ctx, audit.System, name, v); err != nil {
			t.Fatal(err)
		}
	}
	e.GitHub.Install("acme/payments")
	// A done round whose run used the Skael set.
	roundID := uuid.Must(uuid.NewV7())
	e.SQL(t, `INSERT INTO reviews (id, repository_id, pr_number, workflow_id) SELECT gen_random_uuid(), id, 1, 'pr/1' FROM repositories WHERE full_name = 'acme/payments'`)
	e.SQL(t, `INSERT INTO rounds (id, review_id, head_sha, base_sha, trigger, status, verdict) SELECT $1, id, 'h1', 'b1', 'opened', 'done', 'changes_suggested' FROM reviews`, roundID)
	run, tok, err := runner.Create(ctx, e.Deps.Pool, runner.NewRun{RoundID: &roundID, Kind: "round", Spec: runner.Spec{Skael: &runner.Skael{URL: sk.URL, Set: skaeltest.Set, SkillName: skaeltest.SkillName, Hook: true}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// The run spec carries the agent key and never the reporter key (SI-01).
	req, _ := http.NewRequest(http.MethodGet, e.Base+"/api/runner/v1/spec", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(spec), sk.AgentKey) || strings.Contains(string(spec), sk.ReporterKey) {
		t.Fatalf("spec keys: agent %v, reporter %v", strings.Contains(string(spec), sk.AgentKey), strings.Contains(string(spec), sk.ReporterKey))
	}
	result := runner.Result{RunID: run.ID.String(), Kind: "round", Harness: runner.Harness{Name: "pi", Version: "0.73.1"},
		SkaelManifest: &runner.SkaelManifest{Set: skaeltest.Set, ResolutionID: "r1", Skills: []runner.SkaelSkill{{Name: skaeltest.SkillName, Version: 1, SHA256: "x"}}},
		SkillsLoaded:  []runner.SkillLoaded{{Name: skaeltest.SkillName, Version: 1, Stage: "review"}, {Name: skaeltest.SkillName, Version: 1, Stage: "challenge"}}}
	b, _ := json.Marshal(runner.Complete{ExitStatus: "completed", Result: &result})
	req, _ = http.NewRequest(http.MethodPost, e.Base+"/api/runner/v1/complete", strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("complete: %v %v", err, resp.StatusCode)
	}
	rep := e.F.Reporter
	rep.Backoff = 10 * time.Millisecond
	if err := rep.ReportRound(ctx, roundID); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		return sk.Query(t, `SELECT outcome || ' ' || repairs || ' ' || review_verdict FROM runs WHERE consumer = 'sayso' AND external_run_id = '`+roundID.String()+`'`)
	}
	if got := read(); got != "succeeded 0 changes_suggested; none" {
		t.Fatalf("first report: %q", got)
	}
	var o outcomes
	sk.Get(t, "/api/skills/"+skaeltest.SkillName+"/outcomes", &o)
	if len(o.Consumers) != 1 || o.Consumers[0].Consumer != "sayso" || o.Consumers[0].Versions[0].Succeeded != 1 {
		t.Fatalf("skill outcomes %+v", o)
	}
	if n := sk.Query(t, `SELECT count(*) FROM run_skills rs JOIN runs r ON r.id = rs.run_id WHERE r.external_run_id = '`+roundID.String()+`'`); n == "0" {
		t.Fatal("Skael has no run events for the round")
	}
	// After a dismiss, the report replaces the first with repairs 1.
	e.SQL(t, `INSERT INTO steering_signals (id, round_id, type, actor_login) VALUES (gen_random_uuid(), $1, 'dismissed', 'dev-alice')`, roundID)
	if err := rep.ReportRound(ctx, roundID); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "succeeded 1 changes_suggested; none" {
		t.Fatalf("second report: %q", got)
	}
	// The repeat stores no second copy of the run events.
	if n := sk.Query(t, `SELECT count(*) FROM skill_events WHERE consumer = 'sayso'`); n != "2" {
		t.Fatalf("skill events after two reports: %s", n)
	}
	// A failure is retried 5 times, then shows on the Diagnostics page.
	sk.Stop(t)
	if err := rep.ReportRound(ctx, roundID); err == nil {
		t.Fatal("a report to a stopped Skael passed")
	}
	if n := e.Count(t, `SELECT count(*) FROM skael_reports WHERE round_id = $1 AND last_error <> '' AND attempts >= 7`, roundID); n != 1 {
		t.Fatal("the failed report is not recorded")
	}
}
