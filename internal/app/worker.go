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
	"github.com/alternayte/sayso/internal/review/signals"
	"github.com/alternayte/sayso/internal/settings"
)

// WorkerStopTimeout bounds the wait for running activities at shutdown.
// It leaves time for the HTTP shutdown inside ShutdownTimeout.
const WorkerStopTimeout = 25 * time.Second

// StartWorker starts the Temporal workers of the worker role and creates the
// reconciler schedule. The returned function stops polling and waits for
// running activities.
func StartWorker(ctx context.Context, d *Deps, f *Features) (func(), error) {
	w := worker.New(d.Temporal, signals.TaskQueue, worker.Options{WorkerStopTimeout: WorkerStopTimeout})
	w.RegisterWorkflowWithOptions(github.Reconciler, workflow.RegisterOptions{Name: github.ReconcilerWorkflow})
	w.RegisterActivity(&github.ReconcileActivities{Pool: d.Pool, Secrets: f.Secrets, GitHub: f.GitHubClient, Temporal: d.Temporal})
	w.RegisterWorkflowWithOptions(review.PRReview, workflow.RegisterOptions{Name: signals.WorkflowType})
	w.RegisterWorkflowWithOptions(review.ReviewRound, workflow.RegisterOptions{Name: review.RoundWorkflowType})
	w.RegisterActivity(ReviewActivities(d, f))
	limit, err := f.Settings.Int(ctx, settings.WorkerMaxAgentRuns)
	if err != nil {
		return nil, err
	}
	aa, err := AgentActivities(d, f)
	if err != nil {
		return nil, err
	}
	// Each worker runs at most worker_max_agent_runs agent runs (§7.3).
	aw := worker.New(d.Temporal, signals.AgentRunsQueue, worker.Options{WorkerStopTimeout: WorkerStopTimeout, MaxConcurrentActivityExecutionSize: max(limit, 1)})
	aw.RegisterActivity(aa)
	if err := w.Start(); err != nil {
		return nil, fmt.Errorf("temporal worker: %w", err)
	}
	if err := aw.Start(); err != nil {
		w.Stop()
		return nil, fmt.Errorf("temporal agent worker: %w", err)
	}
	stop := func() {
		aw.Stop()
		w.Stop()
	}
	minutes, err := f.Settings.Int(ctx, settings.ReconcileIntervalMinutes)
	if err != nil {
		stop()
		return nil, err
	}
	if err := github.EnsureReconcileSchedule(ctx, d.Temporal, time.Duration(minutes)*time.Minute); err != nil {
		d.Log.Warn("reconciler schedule not created", "error", err)
	}
	d.Log.Info("worker started", "task_queue", signals.TaskQueue, "agent_runs", limit)
	return stop, nil
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
