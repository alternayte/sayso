package steering_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/steering"
)

func TestSCN_OBS_008_ExportCase(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	e.GitHub.Install("acme/payments")
	ctx := context.Background()
	review, round := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	e.SQL(t, `INSERT INTO reviews (id, repository_id, pr_number, workflow_id, author_login) SELECT $1, id, 7, 'pr/7', 'dev-alice' FROM repositories WHERE full_name = 'acme/payments'`, review)
	e.SQL(t, `INSERT INTO rounds (id, review_id, head_sha, base_sha, trigger, status, verdict, risk, config_snapshot, skill_ref, prompt_ref)
		VALUES ($1, $2, 'h1', 'b1', 'opened', 'done', 'blocking', 'high', '{"profiles":{"review":"review"},"spec":{"kind":"round"}}',
		'{"source":"builtin","name":"pr-review","sha256":"abc"}', '{"contract":"1","stages":{"review":"s1"}}')`, round, review)
	f := uuid.Must(uuid.NewV7())
	e.SQL(t, `INSERT INTO findings (id, review_id, fingerprint, seq, severity, category, title, path, start_line, end_line, commit_sha, evidence, status, blocking, first_round_id, last_round_id)
		VALUES ($1, $2, 'fp', 1, 'blocker', 'correctness', 'Balance can overflow', 'ledger/ledger.go', 15, 18, 'h1', '{"kind":"call_path","detail":"Charge -> Post"}', 'dismissed', false, $3, $3)`, f, review, round)
	e.SQL(t, `INSERT INTO steering_signals (id, review_id, round_id, finding_id, type, actor_login, detail) VALUES ($1, $2, $3, $4, 'dismissed', 'dev-alice', '{"reason":"bounded by the API"}')`,
		uuid.Must(uuid.NewV7()), review, round, f)
	e.SQL(t, `INSERT INTO steering_reviews (id, round_id, tags, note, author_login) VALUES ($1, $2, '{false-positive}', 'The API bounds the amount.', 'octo-admin')`, uuid.Must(uuid.NewV7()), round)

	c, err := (&steering.API{Pool: e.Deps.Pool}).Case(ctx, round, "https://github.com")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	for _, k := range []string{"pull_request", "round", "config_snapshot", "skill_ref", "prompt_ref", "findings", "signals", "steering_review"} {
		if doc[k] == nil {
			t.Fatalf("the case has no %s:\n%s", k, raw)
		}
	}
	if c.PullRequest.URL != "https://github.com/acme/payments/pull/7" || len(c.Findings) != 1 || len(c.Signals) != 1 || c.Review == nil || c.Review.Tags[0] != "false-positive" {
		t.Fatalf("case %+v", c)
	}
	b, err := os.ReadFile("../../schemas/case.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rs.Validate(doc); err != nil {
		t.Fatalf("the case does not validate: %v", err)
	}
}
