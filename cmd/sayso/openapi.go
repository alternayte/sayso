package main

import (
	"github.com/spf13/cobra"

	"github.com/alternayte/sayso/internal/app"
)

func openapiCmd() *cobra.Command {
	return &cobra.Command{Use: "openapi", Short: "Print the OpenAPI document", Hidden: true, RunE: func(c *cobra.Command, _ []string) error {
		b, err := app.OpenAPI()
		if err != nil {
			return err
		}
		_, err = c.OutOrStdout().Write(append(b, '\n'))
		return err
	}}
}
