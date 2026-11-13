package storage

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// Retention names (§15.2).
const (
	RetentionWorkflowName = "Retention"
	RetentionSchedule     = "sayso-retention"
)

// RetentionResult counts what a retention run removed.
type RetentionResult struct {
	Payloads  int64 `json:"payloads"`
	Artifacts int64 `json:"artifacts"`
	IndexRows int64 `json:"index_rows"`
}

// Retention deletes payloads and artifacts older than their store's
// retention, and event index rows older than the index retention.
type Retention struct {
	Registry *Registry
	// IndexDays returns the event index retention in days (default 400).
	IndexDays func(ctx context.Context) int
	Now       func() time.Time
}

func (r *Retention) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Run is the retention activity.
func (r *Retention) Run(ctx context.Context) (RetentionResult, error) {
	var res RetentionResult
	pool := r.Registry.Pool
	stores, err := New(pool).ListStores(ctx)
	if err != nil {
		return res, err
	}
	beat := func() {
		if activity.IsActivity(ctx) {
			activity.RecordHeartbeat(ctx, res)
		}
	}
	for _, st := range stores {
		s, err := r.Registry.ByID(ctx, st.ID)
		if err != nil {
			return res, err
		}
		cutoff := r.now().AddDate(0, 0, -int(st.RetentionDays))
		for {
			rows, err := pool.Query(ctx, `SELECT run_id, seq, payload_key FROM run_events WHERE store_id = $1 AND payload_key IS NOT NULL AND at < $2 LIMIT 500`, st.ID, cutoff)
			if err != nil {
				return res, err
			}
			type ev struct {
				run uuid.UUID
				seq int64
				key string
			}
			var batch []ev
			for rows.Next() {
				var e ev
				if err := rows.Scan(&e.run, &e.seq, &e.key); err != nil {
					rows.Close()
					return res, err
				}
				batch = append(batch, e)
			}
			rows.Close()
			if len(batch) == 0 {
				break
			}
			for _, e := range batch {
				if err := s.Delete(ctx, e.key); err != nil && !errors.Is(err, ErrNotFound) {
					return res, err
				}
				if _, err := pool.Exec(ctx, `UPDATE run_events SET payload_key = NULL, store_id = NULL WHERE run_id = $1 AND seq = $2`, e.run, e.seq); err != nil {
					return res, err
				}
				res.Payloads++
			}
			beat()
		}
		for {
			rows, err := pool.Query(ctx, `SELECT id, key FROM artifacts WHERE store_id = $1 AND created_at < $2 LIMIT 500`, st.ID, cutoff)
			if err != nil {
				return res, err
			}
			type art struct {
				id  int64
				key string
			}
			var batch []art
			for rows.Next() {
				var a art
				if err := rows.Scan(&a.id, &a.key); err != nil {
					rows.Close()
					return res, err
				}
				batch = append(batch, a)
			}
			rows.Close()
			if len(batch) == 0 {
				break
			}
			for _, a := range batch {
				if err := s.Delete(ctx, a.key); err != nil && !errors.Is(err, ErrNotFound) {
					return res, err
				}
				if _, err := pool.Exec(ctx, `DELETE FROM artifacts WHERE id = $1`, a.id); err != nil {
					return res, err
				}
				res.Artifacts++
			}
			beat()
		}
	}
	days := 400
	if r.IndexDays != nil {
		if d := r.IndexDays(ctx); d > 0 {
			days = d
		}
	}
	tag, err := pool.Exec(ctx, `DELETE FROM run_events WHERE at < $1 AND payload_key IS NULL`, r.now().AddDate(0, 0, -days))
	if err != nil {
		return res, err
	}
	res.IndexRows = tag.RowsAffected()
	return res, nil
}

// RetentionWorkflow runs the retention activity; a daily schedule starts it.
func RetentionWorkflow(ctx workflow.Context) (RetentionResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 6 * time.Hour, HeartbeatTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	var r *Retention
	var res RetentionResult
	err := workflow.ExecuteActivity(ctx, r.Run).Get(ctx, &res)
	return res, err
}

// EnsureRetentionSchedule creates the daily retention schedule once.
func EnsureRetentionSchedule(ctx context.Context, tc client.Client, taskQueue string) error {
	_, err := tc.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID:      RetentionSchedule,
		Spec:    client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: 24 * time.Hour}}},
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP,
		Action:  &client.ScheduleWorkflowAction{ID: "retention", Workflow: RetentionWorkflowName, TaskQueue: taskQueue, WorkflowRunTimeout: 12 * time.Hour},
	})
	var exists *serviceerror.AlreadyExists
	if err == nil || errors.As(err, &exists) || errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
		return nil
	}
	return err
}
