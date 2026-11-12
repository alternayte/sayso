//go:build contract

package github_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/review"
)

type sig struct {
	Name string
	review.Signal
}

// signals returns the signals in the history of a workflow, in order.
func signals(t *testing.T, tc client.Client, id string) []sig {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	it := tc.GetWorkflowHistory(ctx, id, "", false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var out []sig
	for it.HasNext() {
		ev, err := it.Next()
		if err != nil {
			t.Fatalf("history of %s: %v", id, err)
		}
		a := ev.GetWorkflowExecutionSignaledEventAttributes()
		if a == nil {
			continue
		}
		var s review.Signal
		if err := converter.GetDefaultDataConverter().FromPayloads(a.Input, &s); err != nil {
			t.Fatal(err)
		}
		out = append(out, sig{Name: a.SignalName, Signal: s})
	}
	return out
}

// exists reports whether a workflow with this ID was started.
func exists(t *testing.T, tc client.Client, id string) bool {
	t.Helper()
	_, err := tc.DescribeWorkflow(context.Background(), id, "")
	return err == nil
}

// post sends a raw delivery to the API.
func post(t *testing.T, e *apptest.Env, event, delivery, signature string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.Base+"/webhooks/github", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	req.Header.Set("X-Hub-Signature-256", signature)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// installAcme installs the App on acme/payments and acme/web and sets the
// modes of both to comment.
func installAcme(t *testing.T, e *apptest.Env) {
	t.Helper()
	if _, code := e.GitHub.Install("acme/payments", "acme/web"); code != http.StatusOK {
		t.Fatalf("install delivery: %d", code)
	}
	e.SQL(t, `UPDATE repositories SET mode = 'comment'`)
}

func wfID(e *apptest.Env, repo string, n int) string {
	return review.WorkflowID(e.GitHub.Repo(repo).ID, n)
}
