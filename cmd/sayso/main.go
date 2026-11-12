// Command sayso is the Sayso server, worker, relay and CLI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/alternayte/sayso/internal/app"
)

var version = "dev"

func main() {
	if err := root().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "sayso:", err)
		os.Exit(1)
	}
}

func root() *cobra.Command {
	cmd := &cobra.Command{Use: "sayso", Short: "Self-hosted pull request reviewer", SilenceUsage: true, SilenceErrors: true}
	cmd.AddCommand(serveCmd(), migrateCmd(), secretsCmd(), adminCmd(), doctorCmd(), openapiCmd(), fakeCmd(), agentCmd(), versionCmd())
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print the version", RunE: func(c *cobra.Command, _ []string) error {
		_, err := fmt.Fprintln(c.OutOrStdout(), version)
		return err
	}}
}

func serveCmd() *cobra.Command {
	var role string
	c := &cobra.Command{Use: "serve", Short: "Run the server in a role", RunE: func(c *cobra.Command, _ []string) error {
		r := app.Role(os.Getenv("SAYSO_ROLE"))
		if c.Flags().Changed("role") || r == "" {
			r = app.Role(role)
		}
		parsed, err := app.ParseRole(string(r))
		if err != nil {
			return err
		}
		// The relay has no database and no secret key (SI-15).
		need := app.Need{Database: parsed != app.RoleRelay, SecretKey: parsed != app.RoleRelay}
		cfg, log, red, err := setup(need)
		if err != nil {
			return err
		}
		cfg.Role = parsed
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return app.Serve(ctx, cfg, log, red)
	}}
	c.Flags().StringVar(&role, "role", "all", "all, api, worker or relay")
	return c
}

func migrateCmd() *cobra.Command {
	return &cobra.Command{Use: "migrate", Short: "Apply pending database migrations", RunE: func(c *cobra.Command, _ []string) error {
		_, log, _, err := setup(app.Need{Database: true})
		if err != nil {
			return err
		}
		return app.RunMigrate(c.Context(), os.Getenv("SAYSO_DATABASE_URL"), log)
	}}
}
