package main

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/auth"
)

func adminCmd() *cobra.Command {
	c := &cobra.Command{Use: "admin", Short: "Administer users"}
	var login, role string
	grant := &cobra.Command{
		Use:   "grant",
		Short: "Give a GitHub login a role",
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, log, _, err := setup(app.Need{Database: true})
			if err != nil {
				return err
			}
			r, err := auth.ParseRole(role)
			if err != nil {
				return err
			}
			pool, err := pgxpool.New(c.Context(), cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			if _, err := app.Migrate(c.Context(), pool); err != nil {
				return err
			}
			svc := auth.NewService(pool, auth.Config{}, nil, nil, log)
			if err := svc.Grant(c.Context(), audit.Actor{Kind: "system", Login: "cli"}, login, r); err != nil {
				return err
			}
			log.Info("role granted", "login", login, "role", role)
			return nil
		},
	}
	grant.Flags().StringVar(&login, "github-login", "", "GitHub login")
	grant.Flags().StringVar(&role, "role", "", "admin, maintainer or viewer")
	_ = grant.MarkFlagRequired("github-login")
	_ = grant.MarkFlagRequired("role")
	c.AddCommand(grant)
	return c
}
