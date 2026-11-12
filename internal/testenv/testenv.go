// Package testenv starts the real services that layer I tests need:
// PostgreSQL and the Temporal dev server, each in a testcontainer.
package testenv

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/alternayte/sayso/internal/app"
)

const (
	postgresImage = "postgres:16"
	temporalImage = "temporalio/temporal:1.4.1"
)

// Postgres starts a PostgreSQL 16 container and returns its URL.
// The container stops when the test ends.
func Postgres(t testing.TB) string {
	t.Helper()
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("sayso"),
		tcpostgres.WithUsername("sayso"),
		tcpostgres.WithPassword("sayso"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres url: %v", err)
	}
	return url
}

// FreshDatabase creates a new empty database in the server at url and
// returns its URL. Tests that share one container use it to stay isolated.
func FreshDatabase(t testing.TB, url, name string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", cfg.User, cfg.Password, cfg.Host, cfg.Port, name)
}

// Temporal is a running Temporal dev server.
type Temporal struct {
	Address   string
	container testcontainers.Container
}

// Stop stops the server before the test ends.
func (tp *Temporal) Stop(t testing.TB) {
	t.Helper()
	timeout := 5 * time.Second
	if err := tp.container.Stop(context.Background(), &timeout); err != nil {
		t.Fatalf("stop temporal: %v", err)
	}
}

// TemporalDev starts the Temporal dev server with the default namespace.
// The container stops when the test ends.
func TemporalDev(t testing.TB) *Temporal {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.Run(ctx, temporalImage,
		testcontainers.WithCmd("server", "start-dev", "--ip", "0.0.0.0", "--headless"),
		testcontainers.WithExposedPorts("7233/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("7233/tcp").WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		t.Fatalf("start temporal: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	ep, err := c.PortEndpoint(ctx, "7233/tcp", "")
	if err != nil {
		t.Fatalf("temporal endpoint: %v", err)
	}
	return &Temporal{Address: ep, container: c}
}

// Binary builds cmd/sayso once per test process and returns its path.
func Binary(t testing.TB) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sayso-bin-")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "sayso")
		root, err := moduleRoot()
		if err != nil {
			binErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/sayso")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			binErr = fmt.Errorf("go build: %v: %s", err, out)
		}
	})
	if binErr != nil {
		t.Fatalf("build sayso: %v", binErr)
	}
	return binPath
}

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", err
	}
	return filepath.Dir(strings.TrimSpace(string(out))), nil
}

// FreePort returns a TCP port that is free at the time of the call.
func FreePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// SecretKey returns a valid SAYSO_SECRET_KEY value.
func SecretKey() string {
	return "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
}

var (
	sharedOnce sync.Once
	sharedURL  string
	sharedErr  error
	dbSeq      atomic.Int64
)

// DB returns a pool on a new, migrated database in a PostgreSQL container
// that all tests of the package share. Ryuk removes the container when the
// test process ends.
func DB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	sharedOnce.Do(func() {
		ctx := context.Background()
		c, err := tcpostgres.Run(ctx, postgresImage,
			tcpostgres.WithDatabase("sayso"), tcpostgres.WithUsername("sayso"), tcpostgres.WithPassword("sayso"),
			tcpostgres.BasicWaitStrategies())
		if err != nil {
			sharedErr = err
			return
		}
		sharedURL, sharedErr = c.ConnectionString(ctx, "sslmode=disable")
	})
	if sharedErr != nil {
		t.Fatalf("shared postgres: %v", sharedErr)
	}
	name := fmt.Sprintf("t%d_%d", os.Getpid(), dbSeq.Add(1))
	url := FreshDatabase(t, sharedURL, name)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := app.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}
