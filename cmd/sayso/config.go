package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/config"
)

func configCmd() *cobra.Command {
	c := &cobra.Command{Use: "config", Short: "Export and apply the configuration as YAML"}
	open := func(c *cobra.Command) (*pgxpool.Pool, error) {
		cfg, _, _, err := setup(app.Need{Database: true})
		if err != nil {
			return nil, err
		}
		pool, err := pgxpool.New(c.Context(), cfg.DatabaseURL)
		if err != nil {
			return nil, err
		}
		if _, err := app.Migrate(c.Context(), pool); err != nil {
			pool.Close()
			return nil, err
		}
		return pool, nil
	}
	var file string
	export := &cobra.Command{
		Use:   "export",
		Short: "Write the configuration as YAML, without secret values",
		RunE: func(c *cobra.Command, _ []string) error {
			pool, err := open(c)
			if err != nil {
				return err
			}
			defer pool.Close()
			b, err := (&config.Service{Pool: pool}).Export(c.Context())
			if err != nil {
				return err
			}
			if file == "" {
				_, err = c.OutOrStdout().Write(b)
				return err
			}
			return os.WriteFile(file, b, 0o600)
		},
	}
	export.Flags().StringVar(&file, "file", "", "file to write (default: standard output)")
	var yes bool
	apply := &cobra.Command{
		Use:   "apply",
		Short: "Show the diff of a YAML file and apply it in one transaction",
		RunE: func(c *cobra.Command, _ []string) error {
			b, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			pool, err := open(c)
			if err != nil {
				return err
			}
			defer pool.Close()
			svc := &config.Service{Pool: pool}
			plan, err := svc.Plan(c.Context(), b)
			if err != nil {
				return err
			}
			out := c.OutOrStdout()
			printPlan(out, plan)
			if len(plan) == 0 {
				return nil
			}
			if !yes {
				fmt.Fprint(out, "Apply these changes? Type yes: ")
				line, _ := bufio.NewReader(c.InOrStdin()).ReadString('\n')
				if strings.TrimSpace(line) != "yes" {
					return fmt.Errorf("apply cancelled; nothing changed")
				}
			}
			if _, err := svc.Apply(c.Context(), audit.Actor{Kind: "system", Login: "cli"}, b, plan); err != nil {
				return err
			}
			fmt.Fprintf(out, "Applied %d changes.\n", len(plan))
			return nil
		},
	}
	apply.Flags().StringVar(&file, "file", "", "YAML file to apply")
	apply.Flags().BoolVar(&yes, "yes", false, "apply without the confirmation prompt")
	_ = apply.MarkFlagRequired("file")
	c.AddCommand(export, apply)
	return c
}

func printPlan(w io.Writer, plan []config.ConfigChange) {
	if len(plan) == 0 {
		fmt.Fprintln(w, "No changes.")
		return
	}
	mark := map[string]string{"add": "+", "change": "~", "remove": "-"}
	for _, ch := range plan {
		fmt.Fprintf(w, "%s %s %s\n", mark[ch.Op], ch.Section, ch.Name)
		for _, f := range ch.Fields {
			switch ch.Op {
			case "add":
				fmt.Fprintf(w, "    %s: %s\n", f.Field, oneLine(f.New))
			default:
				fmt.Fprintf(w, "    %s: %s -> %s\n", f.Field, oneLine(f.Old), oneLine(f.New))
			}
		}
	}
}

func oneLine(s string) string {
	if s == "" {
		return "(none)"
	}
	return strings.ReplaceAll(s, "\n", " ")
}
