package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/testenv"
)

// server is one running `sayso serve` process.
type server struct {
	cmd  *exec.Cmd
	base string
	logs *bytes.Buffer
}

func startServe(t *testing.T, dbURL, temporalAddr, role string, extraEnv ...string) *server {
	t.Helper()
	port := testenv.FreePort(t)
	cmd := exec.Command(testenv.Binary(t), "serve", "--role", role)
	cmd.Env = append([]string{
		"PATH=/usr/bin:/bin",
		"SAYSO_DATABASE_URL=" + dbURL,
		"SAYSO_SECRET_KEY=" + testenv.SecretKey(),
		"SAYSO_TEMPORAL_ADDRESS=" + temporalAddr,
		"SAYSO_LISTEN=127.0.0.1:" + fmt.Sprint(port),
		"SAYSO_BASE_URL=http://127.0.0.1:" + fmt.Sprint(port),
	}, extraEnv...)
	logs := &bytes.Buffer{}
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	s := &server{cmd: cmd, base: fmt.Sprintf("http://127.0.0.1:%d", port), logs: logs}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("serve logs (%s):\n%s", s.base, logs.String())
		}
	})
	return s
}

func (s *server) waitStatus(t *testing.T, path string, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	last := 0
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.base + path)
		if err == nil {
			last = resp.StatusCode
			resp.Body.Close()
			if last == want {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s%s: want %d, last %d", s.base, path, want, last)
}

func TestSCN_CORE_001_TwoServersMigrateOnce(t *testing.T) {
	dbURL := testenv.Postgres(t)
	tp := testenv.TemporalDev(t)

	a := startServe(t, dbURL, tp.Address, "all")
	b := startServe(t, dbURL, tp.Address, "all")
	a.waitStatus(t, "/readyz", http.StatusOK, 60*time.Second)
	b.waitStatus(t, "/readyz", http.StatusOK, 60*time.Second)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var applied, distinct int
	err = conn.QueryRow(ctx,
		`SELECT count(*), count(DISTINCT version_id) FROM goose_db_version WHERE version_id > 0 AND is_applied`).
		Scan(&applied, &distinct)
	if err != nil {
		t.Fatal(err)
	}
	want, err := app.MigrationCount()
	if err != nil {
		t.Fatal(err)
	}
	if applied != want || distinct != want {
		t.Fatalf("migrations: applied rows %d, distinct %d, want %d each", applied, distinct, want)
	}
}

func TestSCN_CORE_005_HealthReadyMetrics(t *testing.T) {
	dbURL := testenv.Postgres(t)
	tp := testenv.TemporalDev(t)
	s := startServe(t, dbURL, tp.Address, "all")
	s.waitStatus(t, "/readyz", http.StatusOK, 60*time.Second)
	s.waitStatus(t, "/healthz", http.StatusOK, time.Second)
	s.waitStatus(t, "/metrics", http.StatusOK, time.Second)

	tp.Stop(t)
	s.waitStatus(t, "/readyz", http.StatusServiceUnavailable, 30*time.Second)
	s.waitStatus(t, "/healthz", http.StatusOK, time.Second)
}
