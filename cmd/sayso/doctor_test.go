package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	fakemodel "github.com/alternayte/sayso/internal/fakes/model"

	"github.com/alternayte/sayso/internal/doctor"
	"github.com/alternayte/sayso/internal/testenv"
)

func TestSCN_CORE_006_DoctorReportsFailures(t *testing.T) {
	dbURL := testenv.Postgres(t)
	// One working provider and one unreachable MCP server.
	fm := httptest.NewServer(fakemodel.New().Handler())
	defer fm.Close()
	seedProvider(t, dbURL, "working", fm.URL+"/v1", "sk-working-0001")
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO mcp_servers (name, url) VALUES ('catalog', 'http://127.0.0.1:1/mcp')`); err != nil {
		t.Fatal(err)
	}
	pool.Close()
	cmd := exec.Command(testenv.Binary(t), "doctor", "--json")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "SAYSO_DATABASE_URL=" + dbURL, "SAYSO_SECRET_KEY=" + testenv.SecretKey(), "SAYSO_TEMPORAL_ADDRESS=127.0.0.1:1"}
	out, err := cmd.Output()
	if err == nil {
		t.Fatal("doctor exited 0 with Temporal unreachable")
	}
	var rs []doctor.Result
	if err := json.Unmarshal(out, &rs); err != nil {
		t.Fatalf("doctor output: %v: %s", err, out)
	}
	got := map[string]doctor.Result{}
	for _, r := range rs {
		got[r.Name] = r
	}
	if !got["database"].OK || !got["secret_key"].OK {
		t.Fatalf("database or key check failed: %+v", rs)
	}
	if got["temporal"].OK || got["temporal"].Detail == "" {
		t.Fatalf("temporal check: %+v", got["temporal"])
	}
	if got["github_app"].OK || got["github_app"].Detail != "the GitHub App does not exist yet" {
		t.Fatalf("github_app check: %+v", got["github_app"])
	}
	if !got["provider working"].OK {
		t.Fatalf("provider check: %+v", got["provider working"])
	}
	if r := got["mcp catalog"]; r.OK || !strings.Contains(r.Detail, "127.0.0.1:1") {
		t.Fatalf("mcp check: %+v", r)
	}
	if r := got["runner"]; r.OK || !strings.Contains(r.Detail, "process runner") {
		t.Fatalf("runner check outside the agent image: %+v", r)
	}
}
