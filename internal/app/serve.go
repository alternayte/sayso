package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.temporal.io/sdk/client"
)

// ShutdownTimeout bounds graceful shutdown (§18.3).
const ShutdownTimeout = 30 * time.Second

// Deps are the shared resources of a running process.
type Deps struct {
	Config   *Config
	Log      *slog.Logger
	Redactor *Redactor
	Pool     *pgxpool.Pool
	Temporal client.Client
	Metrics  *prometheus.Registry
}

// Serve runs the process in its role until ctx ends, then shuts down
// within ShutdownTimeout.
func Serve(ctx context.Context, cfg *Config, log *slog.Logger, red *Redactor) error {
	if cfg.Role == RoleRelay {
		return fmt.Errorf("role relay is not available in this build")
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	if err := waitDatabase(ctx, pool, log); err != nil {
		return err
	}
	n, err := Migrate(ctx, pool)
	if err != nil {
		return err
	}
	log.Info("migrations applied", "count", n)

	tc, err := client.NewLazyClient(client.Options{
		HostPort:  cfg.TemporalAddress,
		Namespace: cfg.TemporalNamespace,
		Logger:    log.With("component", "temporal"),
	})
	if err != nil {
		return fmt.Errorf("temporal client: %w", err)
	}
	defer tc.Close()

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	d := &Deps{Config: cfg, Log: log, Redactor: red, Pool: pool, Temporal: tc, Metrics: reg}

	f, err := NewFeatures(ctx, d)
	if err != nil {
		return err
	}
	h := NewHandler(d, f)

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("listening", "addr", ln.Addr().String(), "role", string(cfg.Role))

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}

func waitDatabase(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := pool.Ping(ctx)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("database not reachable: %w", err)
		}
		log.Warn("database not reachable, retrying", "error", err)
		time.Sleep(time.Second)
	}
}

type readiness struct {
	Database   string `json:"database"`
	Migrations string `json:"migrations"`
	Temporal   string `json:"temporal"`
}

func (d *Deps) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	st := readiness{Database: "ok", Migrations: "ok", Temporal: "ok"}
	ok := true
	if err := d.Pool.Ping(ctx); err != nil {
		st.Database, ok = err.Error(), false
	} else if cur, err := MigrationsCurrent(ctx, d.Pool); err != nil {
		st.Migrations, ok = err.Error(), false
	} else if !cur {
		st.Migrations, ok = "pending", false
	}
	if _, err := d.Temporal.CheckHealth(ctx, &client.CheckHealthRequest{}); err != nil {
		st.Temporal, ok = err.Error(), false
	}
	code := http.StatusOK
	if !ok {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, st)
}
