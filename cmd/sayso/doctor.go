package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/doctor"
)

func doctorCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{Use: "doctor", Short: "Check the database, Temporal, GitHub and the other dependencies", RunE: func(c *cobra.Command, _ []string) error {
		cfg, log, red, err := setup(app.Need{Database: true, SecretKey: true})
		if err != nil {
			return err
		}
		d, f, closeAll, err := app.Open(c.Context(), cfg, log, red)
		if err != nil {
			return err
		}
		defer closeAll()
		rs := doctor.Run(c.Context(), app.Checks(d, f))
		out := c.OutOrStdout()
		if asJSON {
			if err := json.NewEncoder(out).Encode(rs); err != nil {
				return err
			}
		} else {
			for _, r := range rs {
				mark := "pass"
				if !r.OK {
					mark = "FAIL"
				}
				if _, err := fmt.Fprintf(out, "%-4s  %-16s %s\n", mark, r.Name, red.Redact(r.Detail)); err != nil {
					return err
				}
			}
		}
		if !doctor.AllOK(rs) {
			return fmt.Errorf("one or more checks failed")
		}
		return nil
	}}
	c.Flags().BoolVar(&asJSON, "json", false, "print the results as JSON")
	return c
}
