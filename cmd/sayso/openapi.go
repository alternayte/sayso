package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/runner"
)

func openapiCmd() *cobra.Command {
	var doc string
	c := &cobra.Command{Use: "openapi", Short: "Print the OpenAPI document", Hidden: true, RunE: func(c *cobra.Command, _ []string) error {
		var b []byte
		var err error
		switch doc {
		case "api":
			b, err = app.OpenAPI()
		case "runner":
			b, err = runner.OpenAPI()
		case "result":
			b, err = resultSchema()
		default:
			return fmt.Errorf("--doc: use api, runner or result")
		}
		if err != nil {
			return err
		}
		_, err = c.OutOrStdout().Write(append(b, '\n'))
		return err
	}}
	c.Flags().StringVar(&doc, "doc", "api", "api, runner, or result (the JSON Schema of result.json)")
	return c
}

// resultSchema turns the Result schema of the runner API into a standalone
// JSON Schema with its referenced schemas under $defs (Appendix C).
func resultSchema() ([]byte, error) {
	raw, err := runner.OpenAPI()
	if err != nil {
		return nil, err
	}
	var doc struct {
		Components struct {
			Schemas map[string]map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	all := doc.Components.Schemas
	defs := map[string]any{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if k == "$ref" {
					if s, ok := val.(string); ok {
						name := strings.TrimPrefix(s, "#/components/schemas/")
						x[k] = "#/$defs/" + name
						if _, seen := defs[name]; !seen {
							defs[name] = all[name]
							walk(all[name])
						}
					}
					continue
				}
				walk(val)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	root := all["Result"]
	if root == nil {
		return nil, fmt.Errorf("the runner API has no Result schema")
	}
	walk(root)
	out := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": "https://sayso.dev/schemas/result.schema.json", "title": "Sayso agent run result"}
	for k, v := range root {
		out[k] = v
	}
	out["$defs"] = defs
	return json.MarshalIndent(out, "", "  ")
}
