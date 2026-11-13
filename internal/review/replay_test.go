package review_test

import (
	"path/filepath"
	"strings"
	"testing"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/alternayte/sayso/internal/review"
	"github.com/alternayte/sayso/internal/review/signals"
)

const replayDir = "../../tests/replay"

func histories(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(replayDir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no histories in %s: %v", replayDir, err)
	}
	return files
}

func replayer(prReview any) worker.WorkflowReplayer {
	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflowWithOptions(prReview, workflow.RegisterOptions{Name: signals.WorkflowType})
	r.RegisterWorkflowWithOptions(review.ReviewRound, workflow.RegisterOptions{Name: review.RoundWorkflowType})
	return r
}

func TestSCN_LIFE_012_Replay(t *testing.T) {
	r := replayer(review.PRReview)
	types := map[string]int{}
	for _, f := range histories(t) {
		if err := r.ReplayWorkflowHistoryFromJSONFile(nil, f); err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
		}
		for _, typ := range []string{signals.WorkflowType, review.RoundWorkflowType} {
			if strings.Contains(f, "-"+typ+"-") {
				types[typ]++
			}
		}
	}
	if types[signals.WorkflowType] == 0 || types[review.RoundWorkflowType] == 0 {
		t.Fatalf("histories per type: %v", types)
	}
}

// reordered reads the debounce before it ensures the review: a change of
// command order without a version marker.
func reordered(ctx workflow.Context, st review.State) error {
	var d int
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 1})
	_ = workflow.ExecuteActivity(ctx, "Debounce").Get(ctx, &d)
	return review.PRReview(ctx, st)
}

func TestSCN_LIFE_012_ReorderFailsReplay(t *testing.T) {
	r := replayer(reordered)
	for _, f := range histories(t) {
		if !strings.Contains(f, "-"+signals.WorkflowType+"-") {
			continue
		}
		err := r.ReplayWorkflowHistoryFromJSONFile(nil, f)
		if err == nil || !strings.Contains(err.Error(), "nondeterministic") {
			t.Fatalf("%s with reordered commands: %v", filepath.Base(f), err)
		}
		return
	}
	t.Fatal("no PRReview history")
}
