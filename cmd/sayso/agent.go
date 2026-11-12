package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
)

// agentRuntime is where the agent image installs the Node runtime.
const agentRuntime = "/opt/sayso/agent/dist/main.js"

func agentCmd() *cobra.Command {
	var script string
	c := &cobra.Command{Use: "agent", Short: "Run the agent runtime for one agent run (runner entry point)", Hidden: true, RunE: func(c *cobra.Command, _ []string) error {
		for _, v := range []string{"SAYSO_API_URL", "SAYSO_RUN_TOKEN", "SAYSO_RUN_ID"} {
			if os.Getenv(v) == "" {
				return fmt.Errorf("%s is not set: the runner sets it", v)
			}
		}
		node, err := exec.LookPath("node")
		if err != nil {
			return fmt.Errorf("node is not on PATH: run sayso agent in the agent image")
		}
		if _, err := os.Stat(script); err != nil {
			return fmt.Errorf("agent runtime %s: %w", script, err)
		}
		abs, err := filepath.Abs(script)
		if err != nil {
			return err
		}
		// Replace this process, so the runner's signals reach node directly.
		return syscall.Exec(node, []string{"node", abs}, os.Environ())
	}}
	c.Flags().StringVar(&script, "runtime", agentRuntime, "path of the agent runtime entry point")
	return c
}
