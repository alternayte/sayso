package app_test

import (
	"context"
	"strings"
	"testing"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/doctor"
)

// The api role has no Kubernetes access: its runner check reports the
// result of a worker with its own runner configuration.
func TestRunnerCheckRunsOnWorker(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	e := apptest.New(t, apptest.Options{Temporal: true, Env: map[string]string{
		"SAYSO_ROLE": "api", "SAYSO_RUNNER": "kubernetes", "SAYSO_K8S_NAMESPACE": "acme-runs",
	}})
	ctx := context.Background()
	runnerResult := func() doctor.Result {
		for _, r := range doctor.Run(ctx, app.Checks(ctx, e.Deps, e.F)) {
			if r.Name == "runner" {
				return r
			}
		}
		t.Fatal("no runner check")
		return doctor.Result{}
	}
	if r := runnerResult(); r.OK || !strings.Contains(r.Detail, "no worker ran the runner check") {
		t.Fatalf("runner check without a worker: %+v", r)
	}

	cfg := *e.Deps.Config
	cfg.Role, cfg.Runner = app.RoleWorker, "process"
	wd := *e.Deps
	wd.Config = &cfg
	stop, err := app.StartWorker(ctx, &wd, e.F)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	if r := runnerResult(); !strings.HasPrefix(r.Detail, "worker: process runner") {
		t.Fatalf("runner check with a worker: %+v", r)
	}
}
