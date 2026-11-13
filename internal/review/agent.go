package review

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"

	"github.com/alternayte/sayso/internal/feeds"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/skills"
)

// AgentActivities run agent runs on the agent-runs task queue (§7.2 step 3).
type AgentActivities struct {
	Pool       *pgxpool.Pool
	Extensions *skills.Extensions
	Feeds      *feeds.Service
	Runner     runner.Runner
	// RunnerKind is process or kubernetes.
	RunnerKind string
	// APIURL is the runner API URL that agent runs call.
	APIURL string
	// Heartbeat is the activity heartbeat interval (default 15 s).
	Heartbeat time.Duration
}

// RunAgentInput names the round, or the queued work, and its admission slot.
type RunAgentInput struct {
	RoundID     uuid.UUID  `json:"round_id"`
	WorkID      *uuid.UUID `json:"work_id,omitempty"`
	AdmissionID uuid.UUID  `json:"admission_id"`
}

// RunAgent creates the agent run of a round from its config snapshot,
// starts it with the runner and waits for its end. Cancellation stops the
// run: the Job is deleted or the process is killed.
func (a *AgentActivities) RunAgent(ctx context.Context, in RunAgentInput) (string, error) {
	var cfgRaw []byte
	var reviewID, repoID uuid.UUID
	var oldRun *uuid.UUID
	kind := "round"
	var err error
	if in.WorkID != nil {
		err = a.Pool.QueryRow(ctx, `SELECT w.spec, w.review_id, rv.repository_id, w.agent_run_id, w.kind FROM agent_work w JOIN reviews rv ON rv.id = w.review_id WHERE w.id = $1`,
			*in.WorkID).Scan(&cfgRaw, &reviewID, &repoID, &oldRun, &kind)
	} else {
		err = a.Pool.QueryRow(ctx, `SELECT ro.config_snapshot, ro.review_id, rv.repository_id, ro.agent_run_id FROM rounds ro JOIN reviews rv ON rv.id = ro.review_id WHERE ro.id = $1`,
			in.RoundID).Scan(&cfgRaw, &reviewID, &repoID, &oldRun)
	}
	if err != nil {
		return "", err
	}
	var cfg Config
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		return "", fmt.Errorf("config snapshot: %w", err)
	}
	// A retry after a worker crash leaves the old run behind.
	if oldRun != nil {
		if _, err := a.Pool.Exec(ctx, `UPDATE agent_runs SET cancel_requested = true, status = CASE WHEN status IN ('queued', 'running') THEN 'failed' ELSE status END,
			exit_reason = CASE WHEN exit_reason = '' THEN 'the worker restarted the run' ELSE exit_reason END WHERE id = $1`, *oldRun); err != nil {
			return "", err
		}
	}
	spec := cfg.Spec
	names := make([]string, 0, len(spec.Extensions))
	for _, e := range spec.Extensions {
		names = append(names, e.Name)
	}
	if spec.Extensions, err = a.Extensions.Spec(ctx, names); err != nil {
		return "", err
	}
	feedNames := make([]string, 0, len(spec.Feeds))
	for _, f := range spec.Feeds {
		feedNames = append(feedNames, f.Name)
	}
	if spec.Feeds, err = a.Feeds.SpecFeeds(ctx, spec.Repository.FullName, feedNames); err != nil {
		return "", err
	}
	if spec.Feeds == nil {
		spec.Feeds = []runner.SpecFeed{}
	}
	nr := runner.NewRun{ReviewID: &reviewID, RepositoryID: &repoID, Kind: kind, Runner: a.RunnerKind, Models: cfg.Models, BudgetUSD: cfg.BudgetUSD, Stages: spec.Stages, Spec: spec}
	if in.WorkID == nil {
		nr.RoundID = &in.RoundID
	}
	run, tok, err := runner.Create(ctx, a.Pool, nr, time.Now())
	if err != nil {
		return "", err
	}
	if in.WorkID != nil {
		_, err = a.Pool.Exec(ctx, `WITH w AS (UPDATE agent_work SET agent_run_id = $2 WHERE id = $1) UPDATE agent_runs SET work_id = $1 WHERE id = $2`, *in.WorkID, run.ID)
	} else {
		_, err = a.Pool.Exec(ctx, `UPDATE rounds SET agent_run_id = $2 WHERE id = $1`, in.RoundID, run.ID)
	}
	if err != nil {
		return "", err
	}
	timeout := time.Duration(spec.Limits.TimeoutSeconds) * time.Second
	h, err := a.Runner.Start(ctx, runner.Job{RunID: run.ID.String(), Token: tok, APIURL: a.APIURL, Image: cfg.Image, Timeout: timeout})
	if err != nil {
		_, _ = a.Pool.Exec(ctx, `UPDATE agent_runs SET status = 'failed', exit_reason = $2, finished_at = now() WHERE id = $1`, run.ID, "start: "+err.Error())
		return "", err
	}
	beat := a.Heartbeat
	if beat == 0 {
		beat = 15 * time.Second
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(beat)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				activity.RecordHeartbeat(ctx, run.ID.String())
				_, _ = a.Pool.Exec(context.WithoutCancel(ctx), `UPDATE admissions SET heartbeat_at = now() WHERE id = $1`, in.AdmissionID)
			}
		}
	}()
	reason, werr := h.Wait(ctx)
	if ctx.Err() != nil {
		stop := context.WithoutCancel(ctx)
		_ = h.Stop(stop)
		_, _ = a.Pool.Exec(stop, `UPDATE agent_runs SET cancel_requested = true, status = CASE WHEN status IN ('queued', 'running') THEN 'cancelled' ELSE status END,
			finished_at = coalesce(finished_at, now()) WHERE id = $1`, run.ID)
		return runner.ExitCancelled, ctx.Err()
	}
	// The runtime calls complete before it exits; a run that ended without
	// it failed, unless Sayso asked it to stop.
	if _, err := a.Pool.Exec(ctx, `UPDATE agent_runs SET status = CASE WHEN cancel_requested THEN 'cancelled' ELSE 'failed' END,
		exit_reason = CASE WHEN cancel_requested THEN 'cancelled' ELSE $2 END, finished_at = now() WHERE id = $1 AND status IN ('queued', 'running')`,
		run.ID, exitText(reason, werr)); err != nil {
		return "", err
	}
	return reason, nil
}

func exitText(reason string, err error) string {
	if err != nil {
		return reason + ": " + err.Error()
	}
	if reason == "" {
		return "the agent run ended without a result"
	}
	return "the agent run ended: " + reason
}
