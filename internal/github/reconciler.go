package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/repos"
	"github.com/alternayte/sayso/internal/review/signals"
)

const (
	// ReconcilerWorkflow is the workflow type that the schedule starts.
	ReconcilerWorkflow = "Reconciler"
	// ReconcilerSchedule is the ID of the Temporal schedule.
	ReconcilerSchedule = "sayso-reconciler"
	// DeliveryWindow is how far back the reconciler reads deliveries.
	DeliveryWindow = 24 * time.Hour
)

// ReconcileResult counts what one reconciler run did.
type ReconcileResult struct {
	Redelivered int `json:"redelivered"`
	Started     int `json:"started"`
}

// Reconciler recovers missed events (§6.4): it asks GitHub to redeliver
// failed deliveries and starts rounds for open pull requests without one.
func Reconciler(ctx workflow.Context) (ReconcileResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	var a *ReconcileActivities
	var res ReconcileResult
	if err := workflow.ExecuteActivity(ctx, a.RedeliverFailed).Get(ctx, &res.Redelivered); err != nil {
		return res, err
	}
	err := workflow.ExecuteActivity(ctx, a.StartMissingRounds).Get(ctx, &res.Started)
	return res, err
}

// ReconcileActivities are the activities of the Reconciler workflow.
type ReconcileActivities struct {
	Pool     *pgxpool.Pool
	Secrets  SecretReader
	GitHub   func(ctx context.Context) (*Client, error)
	Temporal client.Client
	Now      func() time.Time
}

func (a *ReconcileActivities) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// RedeliverFailed asks GitHub to redeliver each failed delivery of the last
// 24 hours that Sayso did not handle. It returns the count of requests.
func (a *ReconcileActivities) RedeliverFailed(ctx context.Context) (int, error) {
	app, err := LoadApp(ctx, a.Pool, a.Secrets)
	if errors.Is(err, ErrNoApp) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	gh, err := a.GitHub(ctx)
	if err != nil {
		return 0, err
	}
	all, err := gh.Deliveries(ctx, app, a.now().Add(-DeliveryWindow))
	if err != nil {
		return 0, err
	}
	// The newest attempt of each delivery decides; a later success makes
	// an earlier failure irrelevant.
	latest := map[string]Delivery{}
	var guids []string
	for _, d := range all {
		if _, seen := latest[d.GUID]; !seen {
			latest[d.GUID] = d
			guids = append(guids, d.GUID)
		}
	}
	known, err := New(a.Pool).KnownDeliveries(ctx, guids)
	if err != nil {
		return 0, err
	}
	handled := map[string]bool{}
	for _, k := range known {
		handled[k] = true
	}
	n := 0
	for _, g := range guids {
		d := latest[g]
		if !d.Failed() || handled[g] {
			continue
		}
		activity.RecordHeartbeat(ctx, g)
		if err := New(a.Pool).RequestRedelivery(ctx, g); err != nil {
			return n, err
		}
		if err := gh.Redeliver(ctx, app, d.ID); err != nil {
			return n, fmt.Errorf("redeliver %s: %w", g, err)
		}
		n++
	}
	if n > 0 {
		_ = audit.Write(ctx, a.Pool, audit.System, "github.redelivery", "", map[string]any{"count": n})
	}
	return n, nil
}

// StartMissingRounds sends head_updated with trigger reconcile for each open,
// non-draft pull request whose head has no queued, running, publishing or
// done round. It returns the count of signals.
func (a *ReconcileActivities) StartMissingRounds(ctx context.Context) (int, error) {
	app, err := LoadApp(ctx, a.Pool, a.Secrets)
	if errors.Is(err, ErrNoApp) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	gh, err := a.GitHub(ctx)
	if err != nil {
		return 0, err
	}
	active, err := repos.New(a.Pool).Active(ctx)
	if err != nil {
		return 0, err
	}
	bot := app.Slug + "[bot]"
	tokens := map[int64]string{}
	n := 0
	for _, r := range active {
		tok, ok := tokens[r.InstallationID]
		if !ok {
			// One token per installation for this activity (§6.1).
			if tok, err = gh.InstallationToken(ctx, app, r.InstallationID, TokenRequest{}); err != nil {
				return n, err
			}
			tokens[r.InstallationID] = tok
		}
		pulls, err := gh.OpenPulls(ctx, tok, r.InstallationID, r.FullName)
		if err != nil {
			return n, fmt.Errorf("list pull requests of %s: %w", r.FullName, err)
		}
		for _, pr := range pulls {
			activity.RecordHeartbeat(ctx, r.FullName)
			if pr.Draft || strings.EqualFold(pr.User.Login, bot) {
				continue
			}
			has, err := New(a.Pool).HasActiveRound(ctx, HasActiveRoundParams{RepositoryID: r.ID, PrNumber: int32(pr.Number), HeadSha: pr.Head.SHA})
			if err != nil {
				return n, err
			}
			if has {
				continue
			}
			s := signals.Signal{Trigger: signals.TriggerReconcile, HeadSHA: pr.Head.SHA, BaseSHA: pr.Base.SHA}
			if err := SignalReview(ctx, a.Temporal, r, pr.Number, signals.SignalHeadUpdated, s, nil); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// EnsureReconcileSchedule creates the reconciler schedule, or updates its
// interval when the setting changed.
func EnsureReconcileSchedule(ctx context.Context, tc client.Client, every time.Duration) error {
	sc := tc.ScheduleClient()
	spec := client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: every}}}
	_, err := sc.Create(ctx, client.ScheduleOptions{
		ID:      ReconcilerSchedule,
		Spec:    spec,
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP,
		Action: &client.ScheduleWorkflowAction{
			ID: "reconciler", Workflow: ReconcilerWorkflow, TaskQueue: signals.TaskQueue,
			WorkflowRunTimeout: 30 * time.Minute,
		},
	})
	var exists *serviceerror.AlreadyExists
	if err == nil {
		return nil
	}
	if !errors.As(err, &exists) && !errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
		return err
	}
	h := sc.GetHandle(ctx, ReconcilerSchedule)
	return h.Update(ctx, client.ScheduleUpdateOptions{DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
		cur := in.Description.Schedule.Spec
		if cur != nil && len(cur.Intervals) == 1 && cur.Intervals[0].Every == every {
			return nil, temporal.ErrSkipScheduleUpdate
		}
		in.Description.Schedule.Spec = &spec
		return &client.ScheduleUpdate{Schedule: &in.Description.Schedule}, nil
	}})
}
