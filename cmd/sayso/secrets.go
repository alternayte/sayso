package main

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/secrets"
)

func secretsCmd() *cobra.Command {
	c := &cobra.Command{Use: "secrets", Short: "Manage stored secrets"}
	c.AddCommand(&cobra.Command{
		Use:   "rekey",
		Short: "Encrypt all stored secrets again with SAYSO_SECRET_KEY_NEW",
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, log, _, err := setup(app.Need{Database: true, SecretKey: true})
			if err != nil {
				return err
			}
			if cfg.SecretKeyNew == nil {
				return fmt.Errorf("SAYSO_SECRET_KEY_NEW is required: set it to the new key")
			}
			pool, err := pgxpool.New(c.Context(), cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			if _, err := app.Migrate(c.Context(), pool); err != nil {
				return err
			}
			if err := secrets.Rekey(c.Context(), pool, cfg.SecretKey, cfg.SecretKeyNew, audit.System); err != nil {
				return err
			}
			log.Info("secrets encrypted with the new key; set SAYSO_SECRET_KEY to the new key and remove SAYSO_SECRET_KEY_NEW")
			return nil
		},
	})
	return c
}
