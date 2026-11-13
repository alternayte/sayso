// Package setup holds the setup checklist of §16.3.
package setup

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/models"
)

// Step is one checklist step.
type Step struct {
	Key  string `json:"key" enum:"app,installed,webhook,provider,profiles,storage,repository,round"`
	Done bool   `json:"done"`
	// Link is the UI route where the admin completes the step.
	Link string `json:"link"`
}

type ChecklistBody struct {
	Steps []Step `json:"steps"`
	// Complete is true after every step was done once, also when a step
	// is not done any more, for example a repository turned off.
	Complete bool `json:"complete"`
}

// Checklist reads the state of each step.
func Checklist(ctx context.Context, pool *pgxpool.Pool) (ChecklistBody, error) {
	count := func(q string) (bool, error) {
		var ok bool
		err := pool.QueryRow(ctx, q).Scan(&ok)
		return ok, err
	}
	steps := []struct {
		key, link, q string
	}{
		{"app", "/settings/github", `SELECT EXISTS (SELECT 1 FROM github_app)`},
		{"installed", "/settings/github", `SELECT EXISTS (SELECT 1 FROM repositories WHERE removed_at IS NULL)`},
		{"webhook", "/settings/github", `SELECT EXISTS (SELECT 1 FROM webhook_deliveries)`},
		{"provider", "/settings/models", ""},
		{"profiles", "/settings/models", `SELECT count(*) = 4 FROM profiles WHERE name IN ('review', 'challenge', 'reply', 'fix') AND jsonb_array_length(targets) > 0`},
		{"storage", "/settings/storage", `SELECT EXISTS (SELECT 1 FROM stores WHERE test_ok)`},
		{"repository", "/repositories", `SELECT EXISTS (SELECT 1 FROM repositories WHERE removed_at IS NULL AND mode <> 'off')`},
		{"round", "/", `SELECT EXISTS (SELECT 1 FROM rounds WHERE status = 'done')`},
	}
	b := ChecklistBody{}
	all := true
	for _, s := range steps {
		var done bool
		var err error
		if s.key == "provider" {
			done, err = providerPassed(ctx, pool)
		} else {
			done, err = count(s.q)
		}
		if err != nil {
			return b, err
		}
		b.Steps = append(b.Steps, Step{Key: s.key, Done: done, Link: s.link})
		all = all && done
	}
	if all {
		if _, err := pool.Exec(ctx, `INSERT INTO setup_completed DEFAULT VALUES ON CONFLICT DO NOTHING`); err != nil {
			return b, err
		}
	}
	err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM setup_completed)`).Scan(&b.Complete)
	return b, err
}

func providerPassed(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	rows, err := pool.Query(ctx, `SELECT smoke FROM providers WHERE smoke IS NOT NULL`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var s models.Smoke
		if json.Unmarshal(raw, &s) == nil && s.OK() {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Register adds the checklist operation.
func Register(api huma.API, pool *pgxpool.Pool, admin map[string]any) {
	huma.Register(api, huma.Operation{OperationID: "get-setup-checklist", Method: http.MethodGet, Path: "/setup/checklist", Tags: []string{"setup"}, Metadata: admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body ChecklistBody }, error) {
			b, err := Checklist(ctx, pool)
			return &struct{ Body ChecklistBody }{b}, err
		})
}
