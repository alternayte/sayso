package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

// Exit reasons of an agent run (§8.2).
const (
	ExitCompleted       = "completed"
	ExitFailed          = "failed"
	ExitDeadline        = "deadline"
	ExitEvicted         = "evicted"
	ExitImagePullFailed = "image_pull_failed"
	ExitCancelled       = "cancelled"
)

// Job is what a runner needs to start one agent run.
type Job struct {
	RunID   string
	Token   string
	APIURL  string
	Image   string
	Timeout time.Duration
}

// Handle is a started agent run.
type Handle interface {
	// Ref is the external reference: a process ID or a Job name.
	Ref() string
	// Wait blocks until the run ends and returns its exit reason.
	Wait(ctx context.Context) (string, error)
	// Stop ends the run and removes what it left behind.
	Stop(ctx context.Context) error
}

// Runner starts agent runs: the process runner or the Kubernetes runner (D-08).
type Runner interface {
	Start(ctx context.Context, j Job) (Handle, error)
}

// proxyVars are the only variables besides the run variables, HOME and PATH
// that the process runner passes on (§8.2, SI-10).
var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"}

// Process runs `sayso agent` as a subprocess in a new temporary directory.
type Process struct {
	// Command is the agent command. Default: this binary with the agent argument.
	Command []string
	// Dir is the parent of the run directories. Default: the system temp dir.
	Dir    string
	Getenv func(string) string
}

// Env returns the environment of a run (SI-10).
func (p *Process) Env(j Job, home string) []string {
	getenv := p.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	env := []string{
		"SAYSO_API_URL=" + j.APIURL,
		"SAYSO_RUN_TOKEN=" + j.Token,
		"SAYSO_RUN_ID=" + j.RunID,
		"HOME=" + home,
		"PATH=" + getenv("PATH"),
	}
	for _, k := range proxyVars {
		if v := getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

type process struct {
	cmd     *exec.Cmd
	dir     string
	done    chan struct{}
	err     error
	stopped atomic.Bool
	timeout time.Duration
	start   time.Time
}

// Start creates the run directory and starts the agent in its own process group.
func (p *Process) Start(_ context.Context, j Job) (Handle, error) {
	dir, err := os.MkdirTemp(p.Dir, "sayso-run-")
	if err != nil {
		return nil, err
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	command := p.Command
	if len(command) == 0 {
		self, err := os.Executable()
		if err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		command = []string{self, "agent"}
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = p.Env(j, home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start agent: %w", err)
	}
	h := &process{cmd: cmd, dir: dir, done: make(chan struct{}), timeout: j.Timeout, start: time.Now()}
	go func() {
		h.err = cmd.Wait()
		close(h.done)
	}()
	return h, nil
}

func (h *process) Ref() string { return fmt.Sprintf("pid:%d", h.cmd.Process.Pid) }

func (h *process) Wait(ctx context.Context) (string, error) {
	var deadline <-chan time.Time
	if h.timeout > 0 {
		t := time.NewTimer(time.Until(h.start.Add(h.timeout)))
		defer t.Stop()
		deadline = t.C
	}
	select {
	case <-h.done:
	case <-deadline:
		_ = h.Stop(context.Background())
		return ExitDeadline, nil
	case <-ctx.Done():
		_ = h.Stop(context.Background())
		return ExitCancelled, ctx.Err()
	}
	defer os.RemoveAll(h.dir)
	if h.stopped.Load() {
		return ExitCancelled, nil
	}
	var ee *exec.ExitError
	if errors.As(h.err, &ee) {
		return ExitFailed, nil
	}
	if h.err != nil {
		return ExitFailed, h.err
	}
	return ExitCompleted, nil
}

// Stop kills the process group and removes the run directory.
func (h *process) Stop(context.Context) error {
	h.stopped.Store(true)
	_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGKILL)
		<-h.done
	}
	// Children that ignored SIGTERM are killed with the group.
	_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGKILL)
	return os.RemoveAll(h.dir)
}
