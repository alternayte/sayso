package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
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
	logs *syncBuffer
}

// syncBuffer is a buffer that the process writes and the test reads at once.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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
	logs := &syncBuffer{}
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

func TestSCN_CORE_009_LogsRedactedAndTraces(t *testing.T) {
	spans := make(chan string, 16)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		spans <- r.URL.Path + " " + string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	dbURL := testenv.Postgres(t)
	tp := testenv.TemporalDev(t)
	const relay = "relay-token-0123456789"
	s := startServe(t, dbURL, tp.Address, "all",
		"OTEL_EXPORTER_OTLP_ENDPOINT="+collector.URL,
		"OTEL_BSP_SCHEDULE_DELAY=100",
		"SAYSO_RELAY_TOKEN="+relay,
		"SAYSO_TEMPORAL_UI_URL=http://user:"+relay+"@temporal-ui:8080",
	)
	s.waitStatus(t, "/readyz", http.StatusOK, 60*time.Second)

	req, _ := http.NewRequest(http.MethodGet, s.base+"/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+relay)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	select {
	case got := <-spans:
		if !strings.HasPrefix(got, "/v1/traces") || !strings.Contains(got, "/api/v1/me") {
			t.Fatalf("collector got %q", got[:min(len(got), 200)])
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the collector received no span")
	}
	if strings.Contains(s.logs.String(), relay) {
		t.Fatalf("logs contain a secret value:\n%s", s.logs.String())
	}
	if !strings.Contains(s.logs.String(), `"level":"INFO"`) {
		t.Fatalf("logs are not JSON:\n%s", s.logs.String())
	}
}
