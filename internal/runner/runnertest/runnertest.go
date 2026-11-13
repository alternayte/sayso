// Package runnertest is a runner for integration tests. It plays the agent
// runtime over the real runner protocol: it reads the spec with the run
// token and completes the run with a result that the test gives.
package runnertest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/alternayte/sayso/internal/runner"
)

// Answer is what a scripted run reports.
type Answer struct {
	// ExitStatus is a Complete exit status. Empty: completed.
	ExitStatus string
	Reason     string
	Result     *runner.Result
	// Hold keeps the run running until it is closed or the run is stopped.
	Hold chan struct{}
	// Manifest is uploaded as manifest.json before the hold, as a Skael pull does.
	Manifest *runner.SkaelManifest
	// Before runs after the spec is read, with the run's job (token, API URL).
	Before func(j runner.Job, spec runner.Spec)
}

// Scripted starts runs that answer with Script.
type Scripted struct {
	Script func(spec runner.Spec) Answer

	mu         sync.Mutex
	running    int
	maxRunning int
	started    []string
	stopped    []string
	order      []string
}

// Start implements runner.Runner.
func (s *Scripted) Start(_ context.Context, j runner.Job) (runner.Handle, error) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &handle{s: s, job: j, cancel: cancel, done: make(chan string, 1)}
	s.mu.Lock()
	s.running++
	s.maxRunning = max(s.maxRunning, s.running)
	s.started = append(s.started, j.RunID)
	s.mu.Unlock()
	go h.run(ctx)
	return h, nil
}

// MaxRunning is the largest number of runs that ran at the same time.
func (s *Scripted) MaxRunning() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxRunning
}

// Running is the number of runs that run now.
func (s *Scripted) Running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Started returns the run IDs in start order.
func (s *Scripted) Started() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.started...)
}

// Stopped returns the run IDs that were stopped.
func (s *Scripted) Stopped() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.stopped...)
}

// SpecOrder returns "<repository>#<number>" of each run, in start order.
func (s *Scripted) SpecOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

type handle struct {
	s      *Scripted
	job    runner.Job
	cancel context.CancelFunc
	done   chan string
	once   sync.Once
}

func (h *handle) Ref() string { return "scripted:" + h.job.RunID }

func (h *handle) call(ctx context.Context, method, path string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		_ = json.NewEncoder(&body).Encode(in)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.job.APIURL+"/api/runner/v1"+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.job.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d", method, path, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (h *handle) finish(reason string) {
	h.once.Do(func() {
		h.s.mu.Lock()
		h.s.running--
		h.s.mu.Unlock()
		h.done <- reason
	})
}

func (h *handle) run(ctx context.Context) {
	var spec runner.Spec
	if err := h.call(ctx, http.MethodGet, "/spec", nil, &spec); err != nil {
		h.finish(runner.ExitFailed)
		return
	}
	h.s.mu.Lock()
	h.s.order = append(h.s.order, fmt.Sprintf("%s#%d", spec.Repository.FullName, spec.PullRequest.Number))
	h.s.mu.Unlock()
	a := h.s.Script(spec)
	if a.Before != nil {
		a.Before(h.job, spec)
	}
	if a.Manifest != nil {
		raw, _ := json.Marshal(a.Manifest)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, h.job.APIURL+"/api/runner/v1/artifacts/manifest.json", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+h.job.Token)
		req.Header.Set("Content-Type", "application/json")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}
	if a.Hold != nil {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
	wait:
		for {
			select {
			case <-a.Hold:
				break wait
			case <-ctx.Done():
				h.finish(runner.ExitCancelled)
				return
			case <-t.C:
				var hb runner.HeartbeatBody
				if h.call(ctx, http.MethodPost, "/heartbeat", struct{}{}, &hb) == nil && hb.Cancel {
					h.finish(runner.ExitCancelled)
					return
				}
			}
		}
	}
	status := a.ExitStatus
	if status == "" {
		status = "completed"
	}
	if a.Result != nil {
		a.Result.RunID = spec.RunID
		a.Result.Kind = spec.Kind
		if a.Result.Harness.Name == "" {
			a.Result.Harness = runner.Harness{Name: "pi", Version: "scripted"}
		}
	}
	if err := h.call(ctx, http.MethodPost, "/complete", runner.Complete{ExitStatus: status, Error: a.Reason, Result: a.Result}, nil); err != nil {
		h.finish(runner.ExitFailed)
		return
	}
	h.finish(runner.ExitCompleted)
}

func (h *handle) Wait(ctx context.Context) (string, error) {
	select {
	case r := <-h.done:
		return r, nil
	case <-ctx.Done():
		return runner.ExitCancelled, ctx.Err()
	}
}

func (h *handle) Stop(context.Context) error {
	h.s.mu.Lock()
	h.s.stopped = append(h.s.stopped, h.job.RunID)
	h.s.mu.Unlock()
	h.cancel()
	return nil
}
