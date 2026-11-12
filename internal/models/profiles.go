package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/runner"
)

// Target is one provider and model of a profile, in fallback order
// (§12.2). Prices are USD per million tokens.
type Target struct {
	Provider       string   `json:"provider"`
	Model          string   `json:"model"`
	Temperature    *float64 `json:"temperature,omitempty"`
	MaxTokens      int      `json:"max_tokens,omitempty" minimum:"0"`
	Reasoning      string   `json:"reasoning,omitempty" enum:",off,low,medium,high"`
	ContextWindow  int      `json:"context_window,omitempty" minimum:"0"`
	PriceIn        float64  `json:"price_in" minimum:"0"`
	PriceOut       float64  `json:"price_out" minimum:"0"`
	PriceCacheRead float64  `json:"price_cache_read" minimum:"0"`
}

type ProfileBody struct {
	Name           string   `json:"name"`
	Targets        []Target `json:"targets"`
	RoundBudgetUSD float64  `json:"round_budget_usd"`
	Default        bool     `json:"default"`
}

func profileBody(p Profile) (ProfileBody, error) {
	b := ProfileBody{Name: p.Name, RoundBudgetUSD: p.RoundBudgetUsd, Default: slices.Contains(DefaultProfiles, p.Name)}
	if err := json.Unmarshal(p.Targets, &b.Targets); err != nil {
		return b, fmt.Errorf("profile %s targets: %w", p.Name, err)
	}
	if b.Targets == nil {
		b.Targets = []Target{}
	}
	return b, nil
}

var profileName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ErrNoToolCalls means a target's provider did not pass the tool call check.
var ErrNoToolCalls = errors.New("did not pass the tool call check of the provider test")

// validateTargets refuses a target whose provider is missing or cannot do
// tool calls (REQ-MOD-002).
func (s *Service) validateTargets(ctx context.Context, ts []Target) error {
	for i, t := range ts {
		if t.Model == "" {
			return fmt.Errorf("target %d: give a model", i+1)
		}
		p, err := New(s.pool).GetProvider(ctx, t.Provider)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("target %d: provider %q does not exist", i+1, t.Provider)
		}
		if err != nil {
			return err
		}
		var sm *Smoke
		if len(p.Smoke) > 0 {
			sm = &Smoke{}
			if err := json.Unmarshal(p.Smoke, sm); err != nil {
				return err
			}
		}
		if !sm.Passed(CheckTools) {
			return fmt.Errorf("target %d: provider %s %w; test the provider first", i+1, t.Provider, ErrNoToolCalls)
		}
	}
	return nil
}

func (s *Service) registerProfiles(api huma.API, acc Access) {
	tags := []string{"models"}
	huma.Register(api, huma.Operation{OperationID: "list-profiles", Method: http.MethodGet, Path: "/profiles", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []ProfileBody }, error) {
			rows, err := New(s.pool).ListProfiles(ctx)
			if err != nil {
				return nil, err
			}
			out := []ProfileBody{}
			for _, p := range rows {
				b, err := profileBody(p)
				if err != nil {
					return nil, err
				}
				out = append(out, b)
			}
			return &struct{ Body []ProfileBody }{out}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "put-profile", Method: http.MethodPut, Path: "/profiles/{name}", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
			Body struct {
				Targets        []Target `json:"targets"`
				RoundBudgetUSD float64  `json:"round_budget_usd,omitempty" minimum:"0"`
			}
		}) (*struct{ Body ProfileBody }, error) {
			if !profileName.MatchString(in.Name) {
				return nil, huma.Error422UnprocessableEntity("name: use lower-case letters, digits and hyphens")
			}
			if err := s.validateTargets(ctx, in.Body.Targets); err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			ts := in.Body.Targets
			if ts == nil {
				ts = []Target{}
			}
			raw, err := json.Marshal(ts)
			if err != nil {
				return nil, err
			}
			var row Profile
			err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
				row, err = New(tx).PutProfile(ctx, PutProfileParams{Name: in.Name, Targets: raw, RoundBudgetUsd: in.Body.RoundBudgetUSD})
				if err != nil {
					return err
				}
				return audit.Write(ctx, tx, acc.Actor(ctx), "profile.put", in.Name, map[string]any{"targets": ts, "round_budget_usd": in.Body.RoundBudgetUSD})
			})
			if err != nil {
				return nil, err
			}
			b, err := profileBody(row)
			return &struct{ Body ProfileBody }{b}, err
		})

	huma.Register(api, huma.Operation{OperationID: "delete-profile", Method: http.MethodDelete, Path: "/profiles/{name}", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{}, error) {
			if slices.Contains(DefaultProfiles, in.Name) {
				return nil, huma.Error409Conflict("the four default profiles cannot be deleted")
			}
			return nil, pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
				if err := New(tx).DeleteProfile(ctx, in.Name); err != nil {
					return err
				}
				return audit.Write(ctx, tx, acc.Actor(ctx), "profile.delete", in.Name, nil)
			})
		})
}

// RunModels returns the model snapshot and the budget of an agent run that
// uses the named profiles. The budget is the largest round budget of them;
// 0 means no budget.
func (s *Service) RunModels(ctx context.Context, profiles ...string) ([]runner.Model, float64, error) {
	var out []runner.Model
	var budget float64
	for _, name := range profiles {
		row, err := New(s.pool).GetProfile(ctx, name)
		if err != nil {
			return nil, 0, notFound(err, "profile "+name)
		}
		b, err := profileBody(row)
		if err != nil {
			return nil, 0, err
		}
		budget = max(budget, b.RoundBudgetUSD)
		for _, t := range b.Targets {
			out = append(out, runner.Model{Profile: name, Provider: t.Provider, Model: t.Model, MaxTokens: t.MaxTokens,
				PriceIn: t.PriceIn, PriceOut: t.PriceOut, PriceCacheRead: t.PriceCacheRead})
		}
	}
	return out, budget, nil
}
