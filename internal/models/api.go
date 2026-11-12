package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/secrets"
)

// DefaultProfiles exist on every instance (REQ-MOD-004).
var DefaultProfiles = []string{"review", "challenge", "reply", "fix"}

// Service is the models feature.
type Service struct {
	pool    *pgxpool.Pool
	secrets *secrets.Store
	http    *http.Client
	redact  func(string) string
	log     *slog.Logger
	limits  *limiter
	// Now is the clock of the proxy and the limits.
	Now func() time.Time
}

// NewService builds the feature. hc is the outbound client with the proxy
// and CA settings.
func NewService(pool *pgxpool.Pool, sec *secrets.Store, hc *http.Client, redact func(string) string, log *slog.Logger) *Service {
	return &Service{pool: pool, secrets: sec, http: hc, redact: redact, log: log, limits: newLimiter(), Now: time.Now}
}

// Access is the metadata and actor of the operations.
type Access struct {
	Admin map[string]any
	Actor func(context.Context) audit.Actor
}

type ProviderInput struct {
	Name            string            `json:"name"`
	APIType         string            `json:"api_type" enum:"openai-completions,openai-responses,anthropic-messages,google-generative-ai"`
	BaseURL         string            `json:"base_url"`
	AuthStyle       string            `json:"auth_style" enum:"bearer,x_api_key,api_key,header"`
	AuthHeader      string            `json:"auth_header,omitempty"`
	Key             *secrets.Input    `json:"key,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Compat          map[string]any    `json:"compat,omitempty"`
	Models          []string          `json:"models,omitempty"`
	MaxConcurrency  int               `json:"max_concurrency,omitempty" minimum:"0"`
	TokensPerMinute int               `json:"tokens_per_minute,omitempty" minimum:"0"`
}

type ProviderBody struct {
	Name            string            `json:"name"`
	APIType         string            `json:"api_type"`
	BaseURL         string            `json:"base_url"`
	AuthStyle       string            `json:"auth_style"`
	AuthHeader      string            `json:"auth_header"`
	Key             secrets.Field     `json:"key"`
	Headers         map[string]string `json:"headers"`
	Compat          map[string]any    `json:"compat"`
	Models          []string          `json:"models"`
	MaxConcurrency  int               `json:"max_concurrency"`
	TokensPerMinute int               `json:"tokens_per_minute"`
	Smoke           *Smoke            `json:"smoke,omitempty"`
	UsedBy          []string          `json:"used_by"`
}

func (s *Service) body(ctx context.Context, p Provider) (ProviderBody, error) {
	c, err := configOf(p)
	if err != nil {
		return ProviderBody{}, err
	}
	key, err := s.secrets.Describe(ctx, p.SecretID)
	if err != nil {
		return ProviderBody{}, err
	}
	b := ProviderBody{Name: p.Name, APIType: p.ApiType, BaseURL: p.BaseUrl, AuthStyle: p.AuthStyle, AuthHeader: p.AuthHeader, Key: key,
		Headers: c.Headers, Compat: map[string]any{}, Models: p.Models, MaxConcurrency: int(p.MaxConcurrency), TokensPerMinute: int(p.TokensPerMinute)}
	if b.Headers == nil {
		b.Headers = map[string]string{}
	}
	if b.Models == nil {
		b.Models = []string{}
	}
	_ = json.Unmarshal(p.Compat, &b.Compat)
	if len(p.Smoke) > 0 {
		var sm Smoke
		if json.Unmarshal(p.Smoke, &sm) == nil {
			b.Smoke = &sm
		}
	}
	if b.UsedBy, err = New(s.pool).ProfilesUsingProvider(ctx, p.Name); err != nil {
		return ProviderBody{}, err
	}
	if b.UsedBy == nil {
		b.UsedBy = []string{}
	}
	return b, nil
}

func (in ProviderInput) config() (Config, json.RawMessage, json.RawMessage, error) {
	c := Config{Name: in.Name, APIType: in.APIType, BaseURL: in.BaseURL, AuthStyle: in.AuthStyle, AuthHeader: in.AuthHeader,
		Headers: in.Headers, Models: in.Models, MaxConcurrency: in.MaxConcurrency, TokensPerMinute: in.TokensPerMinute}
	if c.AuthStyle != AuthHeader {
		c.AuthHeader = ""
	}
	if err := c.Validate(); err != nil {
		return c, nil, nil, err
	}
	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	if c.Models == nil {
		c.Models = []string{}
	}
	h, err := json.Marshal(c.Headers)
	if err != nil {
		return c, nil, nil, err
	}
	compat := in.Compat
	if compat == nil {
		compat = map[string]any{}
	}
	cp, err := json.Marshal(compat)
	return c, h, cp, err
}

func notFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return huma.Error404NotFound(what + " not found")
	}
	return err
}

// Register adds the provider and profile operations for admins.
func (s *Service) Register(api huma.API, acc Access) {
	tags := []string{"models"}
	huma.Register(api, huma.Operation{OperationID: "list-provider-presets", Method: http.MethodGet, Path: "/providers/presets", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []Preset }, error) {
			return &struct{ Body []Preset }{Presets}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "list-providers", Method: http.MethodGet, Path: "/providers", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []ProviderBody }, error) {
			rows, err := New(s.pool).ListProviders(ctx)
			if err != nil {
				return nil, err
			}
			out := []ProviderBody{}
			for _, p := range rows {
				b, err := s.body(ctx, p)
				if err != nil {
					return nil, err
				}
				out = append(out, b)
			}
			return &struct{ Body []ProviderBody }{out}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "create-provider", Method: http.MethodPost, Path: "/providers", Tags: tags, Metadata: acc.Admin, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *struct{ Body ProviderInput }) (*struct{ Body ProviderBody }, error) {
			c, h, cp, err := in.Body.config()
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			if _, err := New(s.pool).GetProvider(ctx, c.Name); err == nil {
				return nil, huma.Error409Conflict("a provider with this name exists")
			}
			actor := acc.Actor(ctx)
			sid, err := s.secrets.Apply(ctx, actor, secretName(c.Name), in.Body.Key, nil)
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			var row Provider
			err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
				row, err = New(tx).InsertProvider(ctx, InsertProviderParams{Name: c.Name, ApiType: c.APIType, BaseUrl: c.BaseURL, AuthStyle: c.AuthStyle,
					AuthHeader: c.AuthHeader, SecretID: sid, Headers: h, Compat: cp, Models: c.Models, MaxConcurrency: int32(c.MaxConcurrency), TokensPerMinute: int32(c.TokensPerMinute)})
				if err != nil {
					return err
				}
				return audit.Write(ctx, tx, actor, "provider.create", c.Name, map[string]any{"api_type": c.APIType, "base_url": c.BaseURL})
			})
			if err != nil {
				return nil, err
			}
			b, err := s.body(ctx, row)
			return &struct{ Body ProviderBody }{b}, err
		})

	huma.Register(api, huma.Operation{OperationID: "update-provider", Method: http.MethodPut, Path: "/providers/{name}", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
			Body ProviderInput
		}) (*struct{ Body ProviderBody }, error) {
			in.Body.Name = in.Name
			c, h, cp, err := in.Body.config()
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			cur, err := New(s.pool).GetProvider(ctx, in.Name)
			if err != nil {
				return nil, notFound(err, "provider")
			}
			actor := acc.Actor(ctx)
			sid, err := s.secrets.Apply(ctx, actor, secretName(c.Name), in.Body.Key, cur.SecretID)
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			var row Provider
			err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
				row, err = New(tx).UpdateProvider(ctx, UpdateProviderParams{Name: c.Name, ApiType: c.APIType, BaseUrl: c.BaseURL, AuthStyle: c.AuthStyle,
					AuthHeader: c.AuthHeader, SecretID: sid, Headers: h, Compat: cp, Models: c.Models, MaxConcurrency: int32(c.MaxConcurrency), TokensPerMinute: int32(c.TokensPerMinute)})
				if err != nil {
					return err
				}
				return audit.Write(ctx, tx, actor, "provider.update", c.Name, map[string]any{"api_type": c.APIType, "base_url": c.BaseURL, "key_changed": in.Body.Key != nil && (in.Body.Key.Value != "" || in.Body.Key.Env != "")})
			})
			if err != nil {
				return nil, err
			}
			b, err := s.body(ctx, row)
			return &struct{ Body ProviderBody }{b}, err
		})

	huma.Register(api, huma.Operation{OperationID: "delete-provider", Method: http.MethodDelete, Path: "/providers/{name}", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{}, error) {
			cur, err := New(s.pool).GetProvider(ctx, in.Name)
			if err != nil {
				return nil, notFound(err, "provider")
			}
			used, err := New(s.pool).ProfilesUsingProvider(ctx, in.Name)
			if err != nil {
				return nil, err
			}
			if len(used) > 0 {
				return nil, huma.Error409Conflict(fmt.Sprintf("profiles %v use this provider: remove it from them first", used))
			}
			actor := acc.Actor(ctx)
			err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
				if err := New(tx).DeleteProvider(ctx, in.Name); err != nil {
					return err
				}
				return audit.Write(ctx, tx, actor, "provider.delete", in.Name, nil)
			})
			if err != nil {
				return nil, err
			}
			if cur.SecretID != nil {
				return nil, s.secrets.Remove(ctx, actor, secretName(in.Name))
			}
			return nil, nil
		})

	huma.Register(api, huma.Operation{OperationID: "test-provider", Method: http.MethodPost, Path: "/providers/{name}/test", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
			Body struct {
				Model string `json:"model,omitempty" doc:"Model to test. Default: the first model of the provider."`
			}
		}) (*struct{ Body Smoke }, error) {
			sm, err := s.Test(ctx, in.Name, in.Body.Model)
			if err != nil {
				return nil, err
			}
			return &struct{ Body Smoke }{sm}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "list-provider-models", Method: http.MethodGet, Path: "/providers/{name}/models", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{ Body []string }, error) {
			ms, err := s.UpstreamModels(ctx, in.Name)
			if err != nil {
				return nil, err
			}
			return &struct{ Body []string }{ms}, nil
		})

	s.registerProfiles(api, acc)
}

// Test runs the smoke test and stores the result (REQ-MOD-002).
func (s *Service) Test(ctx context.Context, name, model string) (Smoke, error) {
	row, err := New(s.pool).GetProvider(ctx, name)
	if err != nil {
		return Smoke{}, notFound(err, "provider")
	}
	c, err := configOf(row)
	if err != nil {
		return Smoke{}, err
	}
	if model == "" && len(c.Models) > 0 {
		model = c.Models[0]
	}
	if model == "" {
		return Smoke{}, huma.Error422UnprocessableEntity("give a model to test, or add a model to the provider")
	}
	key, err := s.Key(ctx, c)
	if err != nil {
		return Smoke{}, huma.Error422UnprocessableEntity(s.redact(err.Error()))
	}
	tctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	sm := RunSmoke(tctx, s.http, c, key, model, s.redact)
	raw, err := json.Marshal(sm)
	if err != nil {
		return Smoke{}, err
	}
	if err := New(s.pool).SetSmoke(ctx, SetSmokeParams{Name: name, Smoke: raw}); err != nil {
		return Smoke{}, err
	}
	return sm, nil
}

// UpstreamModels lists the models of a provider from GET /models when the
// API offers it, else the typed list.
func (s *Service) UpstreamModels(ctx context.Context, name string) ([]string, error) {
	row, err := New(s.pool).GetProvider(ctx, name)
	if err != nil {
		return nil, notFound(err, "provider")
	}
	c, err := configOf(row)
	if err != nil {
		return nil, err
	}
	typed := append([]string{}, c.Models...)
	if c.APIType == GoogleGenerative {
		return typed, nil
	}
	key, err := s.Key(ctx, c)
	if err != nil {
		return typed, nil
	}
	u, err := c.Endpoint("models", "")
	if err != nil {
		return typed, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return typed, nil
	}
	c.SetCredential(req.Header, key)
	resp, err := s.http.Do(req)
	if err != nil {
		return typed, nil
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out) != nil {
		return typed, nil
	}
	for _, m := range out.Data {
		if !slices.Contains(typed, m.ID) {
			typed = append(typed, m.ID)
		}
	}
	slices.Sort(typed)
	return typed, nil
}

// Names returns the provider names.
func (s *Service) Names(ctx context.Context) ([]string, error) {
	rows, err := New(s.pool).ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out, nil
}

// Check makes one plain call to a provider with its first model, for the
// diagnostics (§18.3).
func (s *Service) Check(ctx context.Context, name string) (string, error) {
	row, err := New(s.pool).GetProvider(ctx, name)
	if err != nil {
		return "", err
	}
	c, err := configOf(row)
	if err != nil {
		return "", err
	}
	if len(c.Models) == 0 {
		return "", fmt.Errorf("the provider has no model to call")
	}
	key, err := s.Key(ctx, c)
	if err != nil {
		return "", err
	}
	sm := &smoker{c: c, key: key, model: c.Models[0], http: s.http}
	detail, err := sm.plain(ctx)
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", c.BaseURL, c.Models[0], err)
	}
	return c.Models[0] + " " + detail, nil
}
