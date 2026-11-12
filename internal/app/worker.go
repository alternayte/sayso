package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/review"
	"github.com/alternayte/sayso/internal/settings"
)

// WorkerStopTimeout bounds the wait for running activities at shutdown.
// It leaves time for the HTTP shutdown inside ShutdownTimeout.
const WorkerStopTimeout = 25 * time.Second

// StartWorker starts the Temporal workers of the worker role and creates the
// reconciler schedule. The returned function stops polling and waits for
// running activities.
func StartWorker(ctx context.Context, d *Deps, f *Features) (func(), error) {
	w := worker.New(d.Temporal, review.TaskQueue, worker.Options{WorkerStopTimeout: WorkerStopTimeout})
	w.RegisterWorkflowWithOptions(github.Reconciler, workflow.RegisterOptions{Name: github.ReconcilerWorkflow})
	w.RegisterActivity(&github.ReconcileActivities{Pool: d.Pool, Secrets: f.Secrets, GitHub: f.GitHubClient, Temporal: d.Temporal})
	if err := w.Start(); err != nil {
		return nil, fmt.Errorf("temporal worker: %w", err)
	}
	minutes, err := f.Settings.Int(ctx, settings.ReconcileIntervalMinutes)
	if err != nil {
		w.Stop()
		return nil, err
	}
	if err := github.EnsureReconcileSchedule(ctx, d.Temporal, time.Duration(minutes)*time.Minute); err != nil {
		d.Log.Warn("reconciler schedule not created", "error", err)
	}
	d.Log.Info("worker started", "task_queue", review.TaskQueue)
	return w.Stop, nil
}

// ServeRelay runs the relay role. It opens no database and writes nothing
// to disk (SI-15).
func ServeRelay(ctx context.Context, cfg *Config, log *slog.Logger) error {
	if cfg.RelayToken == "" {
		return fmt.Errorf("SAYSO_RELAY_TOKEN is required for the relay role")
	}
	r := github.NewRelay(cfg.RelayToken, log.With("component", "relay"))
	return listenAndDrain(ctx, cfg, log, r.Handler(), nil)
}
