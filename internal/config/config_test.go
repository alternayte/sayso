package config_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/config"
)

const providerKey = "sk-acme-provider-value-9f3c"

// configured is an instance with every section set, secrets as stored
// values and as environment references.
func configured(t *testing.T) *apptest.Env {
	t.Helper()
	ctx := context.Background()
	e := apptest.New(t, apptest.Options{})
	if _, code := e.GitHub.Install("acme/payments", "acme/web"); code != http.StatusOK {
		t.Fatalf("install: %d", code)
	}
	secret(t, e)
	e.SQL(t, `INSERT INTO providers (name, api_type, base_url, auth_style, secret_id, headers, models, max_concurrency)
		VALUES ('acme-llm', 'openai-completions', 'https://llm.acme.test/v1', 'bearer', (SELECT id FROM secrets WHERE name = 'provider.acme-llm.key'), '{"X-Team":"payments"}', '{fake-large}', 4)`)
	e.SQL(t, `UPDATE profiles SET targets = '[{"provider":"acme-llm","model":"fake-large","price_in":1,"price_out":2,"price_cache_read":0}]', round_budget_usd = 2.5 WHERE name = 'review'`)
	e.SQL(t, `INSERT INTO profiles (name, targets) VALUES ('deep', '[{"provider":"acme-llm","model":"fake-large","price_in":0,"price_out":0,"price_cache_read":0}]')`)
	e.SQL(t, `INSERT INTO secrets (id, name, kind, env_name, updated_by) VALUES (gen_random_uuid(), 'mcp.acme-docs.secret', 'env_ref', 'ACME_DOCS_TOKEN', 'test')`)
	e.SQL(t, `INSERT INTO mcp_servers (name, url, secret_header, secret_id, allow_tools) VALUES ('acme-docs', 'https://mcp.acme.test', 'Authorization',
		(SELECT id FROM secrets WHERE name = 'mcp.acme-docs.secret'), '{search}')`)
	e.SQL(t, `INSERT INTO package_feeds (name, kind, upstream_url, repos) VALUES ('acme-npm', 'npm', 'https://npm.acme.test', '{acme/payments}')`)
	e.SQL(t, `INSERT INTO extensions (name, spec, env, enabled, repositories) VALUES ('acme-lint', 'npm:@acme/pi-lint@1.2.0', '{"LEVEL":"strict"}', true, '{acme/payments}')`)
	e.SQL(t, `INSERT INTO agent_images (name, image, is_default) VALUES ('node22', 'ghcr.io/acme/agent-node:22', true)`)
	e.SQL(t, `UPDATE repositories SET mode = 'comment', mcp_servers = '{acme-docs}', feeds = '{acme-npm}', profile_review = 'deep', agent_image = 'node22' WHERE full_name = 'acme/payments'`)
	e.SQL(t, `INSERT INTO team_roles (team_slug, role) VALUES ('platform', 'admin'), ('payments', 'maintainer')`)
	if _, err := e.F.Settings.Set(ctx, audit.System, "max_findings_per_round", []byte("12")); err != nil {
		t.Fatal(err)
	}
	return e
}

func secret(t *testing.T, e *apptest.Env) {
	t.Helper()
	if _, err := e.F.Secrets.Put(context.Background(), audit.System, "provider.acme-llm.key", providerKey); err != nil {
		t.Fatal(err)
	}
}

func export(t *testing.T, e *apptest.Env) []byte {
	t.Helper()
	b, err := (&config.Service{Pool: e.Deps.Pool}).Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSCN_CFG_004_ExportApplyRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := configured(t)
	out := export(t, src)
	if bytes.Contains(out, []byte(providerKey)) {
		t.Fatal("the export holds a secret value")
	}
	for _, want := range []string{"secret: provider.acme-llm.key", "env: ACME_DOCS_TOKEN", "full_name: acme/payments", "team: platform"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("the export has no %q:\n%s", want, out)
		}
	}

	dst := apptest.New(t, apptest.Options{})
	if _, code := dst.GitHub.Install("acme/payments", "acme/web"); code != http.StatusOK {
		t.Fatalf("install: %d", code)
	}
	secret(t, dst)
	svc := &config.Service{Pool: dst.Deps.Pool, LearnEnv: dst.F.Secrets.LearnEnv}
	plan, err := svc.Plan(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) == 0 {
		t.Fatal("the plan is empty")
	}
	if _, err := svc.Apply(ctx, audit.System, out, plan); err != nil {
		t.Fatal(err)
	}
	if again := export(t, dst); !bytes.Equal(again, out) {
		t.Fatalf("the second export differs:\n--- first\n%s\n--- second\n%s", out, again)
	}
	if again, err := svc.Plan(ctx, out); err != nil || len(again) != 0 {
		t.Fatalf("a second plan has changes %v: %v", again, err)
	}
}

func TestSCN_CFG_004_MissingSecretChangesNothing(t *testing.T) {
	ctx := context.Background()
	out := export(t, configured(t))
	dst := apptest.New(t, apptest.Options{})
	if _, code := dst.GitHub.Install("acme/payments", "acme/web"); code != http.StatusOK {
		t.Fatalf("install: %d", code)
	}
	before := export(t, dst)
	_, err := (&config.Service{Pool: dst.Deps.Pool}).Apply(ctx, audit.System, out, nil)
	if err == nil || !strings.Contains(err.Error(), "provider.acme-llm.key") {
		t.Fatalf("apply: %v", err)
	}
	if after := export(t, dst); !bytes.Equal(before, after) {
		t.Fatal("a failed apply changed the configuration")
	}
}

func TestApplyRefusesAChangedPlan(t *testing.T) {
	ctx := context.Background()
	e := configured(t)
	out := export(t, e)
	svc := &config.Service{Pool: e.Deps.Pool}
	next := strings.Replace(string(out), "round_budget_usd: 2.5", "round_budget_usd: 3", 1)
	plan, err := svc.Plan(ctx, []byte(next))
	if err != nil || len(plan) != 1 || plan[0].Section != "profiles" || plan[0].Name != "review" {
		t.Fatalf("plan %+v: %v", plan, err)
	}
	e.SQL(t, `UPDATE profiles SET round_budget_usd = 4 WHERE name = 'review'`)
	if _, err := svc.Apply(ctx, audit.System, []byte(next), plan); !errors.Is(err, config.ErrChanged) {
		t.Fatalf("apply: %v", err)
	}
}
