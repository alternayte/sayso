package review

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/review/signals"
	"github.com/alternayte/sayso/internal/runner"
)

// RunnerTestWorkflowType is the name of the runner test workflow (§16.3).
const RunnerTestWorkflowType = "RunnerTest"

// RunnerTestResult is the outcome of a no-op agent run.
type RunnerTestResult struct {
	RunID          string `json:"run_id"`
	Status         string `json:"status"`
	ExitReason     string `json:"exit_reason"`
	HarnessVersion string `json:"harness_version"`
	DurationMS     int64  `json:"duration_ms"`
	// Skael is the outcome of the dry-run pull when the test included Skael.
	Skael *SkaelTest `json:"skael,omitempty"`
}

// SkaelTest is the outcome of a dry-run pull of the default set.
type SkaelTest struct {
	Set    string `json:"set"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// RunnerTestInput selects the test: the runner alone, or the runner with a
// dry-run Skael pull of the default set.
type RunnerTestInput struct {
	Skael bool `json:"skael"`
}

// RunnerTestWorkflow starts one no-op agent run on a worker.
func RunnerTestWorkflow(ctx workflow.Context, in RunnerTestInput) (RunnerTestResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: signals.AgentRunsQueue, StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	var a *AgentActivities
	var res RunnerTestResult
	err := workflow.ExecuteActivity(ctx, a.RunnerTest, in).Get(ctx, &res)
	return res, err
}

// RunnerTest starts a no-op agent run with the default image and waits for it.
func (a *AgentActivities) RunnerTest(ctx context.Context, in RunnerTestInput) (RunnerTestResult, error) {
	start := time.Now()
	var image string
	_ = a.Pool.QueryRow(ctx, `SELECT image FROM agent_images WHERE is_default`).Scan(&image)
	spec := runner.Spec{Kind: "noop"}
	if in.Skael {
		var sk runner.Skael
		if err := a.Pool.QueryRow(ctx, `SELECT coalesce((SELECT value #>> '{}' FROM settings WHERE key = 'skael_url'), ''),
			coalesce((SELECT value #>> '{}' FROM settings WHERE key = 'skael_default_set'), ''),
			coalesce((SELECT value #>> '{}' FROM settings WHERE key = 'skael_review_skill'), '')`).Scan(&sk.URL, &sk.Set, &sk.SkillName); err != nil {
			return RunnerTestResult{}, err
		}
		if sk.URL == "" || sk.Set == "" {
			return RunnerTestResult{Status: "failed", ExitReason: "skael_url and skael_default_set must be set", Skael: &SkaelTest{Set: sk.Set}}, nil
		}
		spec.Skael = &sk
	}
	run, tok, err := runner.Create(ctx, a.Pool, runner.NewRun{Kind: "noop", Runner: a.RunnerKind, Spec: spec}, start)
	if err != nil {
		return RunnerTestResult{}, err
	}
	res := RunnerTestResult{RunID: run.ID.String()}
	h, err := a.Runner.Start(ctx, runner.Job{RunID: run.ID.String(), Token: tok, APIURL: a.APIURL, Image: image, Timeout: 4 * time.Minute})
	if err != nil {
		_, _ = a.Pool.Exec(ctx, `UPDATE agent_runs SET status = 'failed', exit_reason = $2, finished_at = now() WHERE id = $1`, run.ID, "start: "+err.Error())
		res.Status, res.ExitReason = "failed", "start: "+err.Error()
		return res, nil
	}
	reason, werr := h.Wait(ctx)
	_, _ = a.Pool.Exec(context.WithoutCancel(ctx), `UPDATE agent_runs SET status = 'failed', exit_reason = $2, finished_at = now() WHERE id = $1 AND status IN ('queued', 'running')`,
		run.ID, exitText(reason, werr))
	if err := a.Pool.QueryRow(ctx, `SELECT status, exit_reason, harness_version FROM agent_runs WHERE id = $1`, run.ID).
		Scan(&res.Status, &res.ExitReason, &res.HarnessVersion); err != nil {
		return res, err
	}
	res.DurationMS = time.Since(start).Milliseconds()
	if spec.Skael != nil {
		res.Skael = &SkaelTest{Set: spec.Skael.Set, Detail: "the run reported no Skael pull"}
		var typ, preview string
		err := a.Pool.QueryRow(ctx, `SELECT type, preview FROM run_events WHERE run_id = $1 AND name IN ('skael '||$2, 'skael pull') ORDER BY seq DESC LIMIT 1`,
			run.ID, spec.Skael.Set).Scan(&typ, &preview)
		if err == nil {
			res.Skael.OK, res.Skael.Detail = typ == "skill_load", preview
		}
	}
	return res, nil
}

// RegisterRunnerTest adds the runner test button operation.
func RegisterRunnerTest(api huma.API, pool *pgxpool.Pool, tc func() client.Client, admin map[string]any, actor func(context.Context) audit.Actor) {
	run := func(ctx context.Context, in RunnerTestInput) (*struct{ Body RunnerTestResult }, error) {
		c := tc()
		if c == nil {
			return nil, huma.Error503ServiceUnavailable("Temporal is not connected")
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		wr, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "runner-test-" + uuid.NewString(), TaskQueue: signals.TaskQueue,
			WorkflowExecutionTimeout: 6 * time.Minute}, RunnerTestWorkflowType, in)
		if err != nil {
			return nil, err
		}
		var res RunnerTestResult
		if err := wr.Get(ctx, &res); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, huma.Error504GatewayTimeout("the runner test did not finish in 5 minutes; check that a worker runs")
			}
			return nil, err
		}
		return &struct{ Body RunnerTestResult }{res}, audit.Write(ctx, pool, actor(ctx), "runner.test", res.RunID, map[string]any{"status": res.Status, "skael": in.Skael})
	}
	huma.Register(api, huma.Operation{OperationID: "test-runner", Method: http.MethodPost, Path: "/runner/test", Tags: []string{"runner"}, Metadata: admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body RunnerTestResult }, error) {
			return run(ctx, RunnerTestInput{})
		})
	huma.Register(api, huma.Operation{OperationID: "test-skael", Method: http.MethodPost, Path: "/skael/test", Tags: []string{"skills"}, Metadata: admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body RunnerTestResult }, error) {
			return run(ctx, RunnerTestInput{Skael: true})
		})
}
