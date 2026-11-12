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
		return ServeRelay(ctx, cfg, log)
	}
	d, f, closeAll, err := Open(ctx, cfg, log, red)
	if err != nil {
		return err
	}
	defer closeAll()
	if err := RegisterNamespace(ctx, d, f.Settings); err != nil {
		log.Warn("temporal namespace not registered", "error", err)
	}
	var h http.Handler
	if cfg.Role.RunsAPI() {
		h = traced(NewHandler(d, f))
	} else {
		h = probeHandler(d)
	}
	flush, err := StartTelemetry(ctx)
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	defer func() { _ = flush(context.Background()) }()

	var stopWorker func()
	if cfg.Role.RunsWorker() {
		if stopWorker, err = StartWorker(ctx, d, f); err != nil {
			return err
		}
	}
	if cfg.Role.RunsAPI() {
		defer f.MCPProxy.Close()
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					f.MCPProxy.Sweep(ctx)
				}
			}
		}()
	}
	if cfg.Role.RunsAPI() && f.Relay != nil {
		rctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go f.Relay.Run(rctx)
	}
	return listenAndDrain(ctx, cfg, log, h, stopWorker)
}

// listenAndDrain serves HTTP until ctx ends. Then it stops the Temporal
// worker from polling, lets running activities and requests finish, and
// returns within ShutdownTimeout (REQ-CORE-009).
func listenAndDrain(ctx context.Context, cfg *Config, log *slog.Logger, h http.Handler, stopWorker func()) error {
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
	drained := make(chan struct{})
	go func() {
		if stopWorker != nil {
			stopWorker()
		}
		close(drained)
	}()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http shutdown: %w", err)
	}
	select {
	case <-drained:
	case <-sctx.Done():
		return errors.New("the worker did not stop within the shutdown time")
	}
	log.Info("stopped")
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

// Open connects to the database and Temporal, applies migrations and builds
// the features. The returned function closes the connections.
func Open(ctx context.Context, cfg *Config, log *slog.Logger, red *Redactor) (*Deps, *Features, func(), error) {
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("database: %w", err)
	}
	if err := waitDatabase(ctx, pool, log); err != nil {
		pool.Close()
		return nil, nil, nil, err
	}
	n, err := Migrate(ctx, pool)
	if err != nil {
		pool.Close()
		return nil, nil, nil, err
	}
	log.Info("migrations applied", "count", n)

	tc, err := client.NewLazyClient(client.Options{
		HostPort:  cfg.TemporalAddress,
		Namespace: cfg.TemporalNamespace,
		Logger:    log.With("component", "temporal"),
	})
	if err != nil {
		pool.Close()
		return nil, nil, nil, fmt.Errorf("temporal client: %w", err)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	d := &Deps{Config: cfg, Log: log, Redactor: red, Pool: pool, Temporal: tc, Metrics: reg}

	f, err := NewFeatures(ctx, d)
	if err != nil {
		tc.Close()
		pool.Close()
		return nil, nil, nil, err
	}
	return d, f, func() { tc.Close(); pool.Close() }, nil
}
