package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/secrets"
	"github.com/alternayte/sayso/internal/settings"
	builtinskill "github.com/alternayte/sayso/skills"
)

type SkaelInput struct {
	URL         string         `json:"url"`
	DefaultSet  string         `json:"default_set"`
	AllowedSets []string       `json:"allowed_sets"`
	ReviewSkill string         `json:"review_skill"`
	Hook        bool           `json:"activation_hook"`
	AgentKey    *secrets.Input `json:"agent_key,omitempty" doc:"skills:read, sets:read and events:write; it reaches agent runs"`
	ReporterKey *secrets.Input `json:"reporter_key,omitempty" doc:"events:write and outcomes:write; it never leaves the API"`
}

type SkaelBody struct {
	URL         string        `json:"url"`
	DefaultSet  string        `json:"default_set"`
	AllowedSets []string      `json:"allowed_sets"`
	ReviewSkill string        `json:"review_skill"`
	Hook        bool          `json:"activation_hook"`
	AgentKey    secrets.Field `json:"agent_key"`
	ReporterKey secrets.Field `json:"reporter_key"`
}

type SkillsBody struct {
	Builtin map[string]string `json:"builtin" doc:"Files of the built-in review skill"`
	Skael   SkaelBody         `json:"skael"`
}

// API is the skills settings page (Appendix D /settings/skills).
type API struct {
	Pool     *pgxpool.Pool
	Settings *settings.Store
	Secrets  *secrets.Store
}

func (a *API) read(ctx context.Context) (SkillsBody, error) {
	var b SkillsBody
	var err error
	if b.Builtin, err = builtinskill.Builtin(); err != nil {
		return b, err
	}
	s := &b.Skael
	for _, kv := range []struct {
		key string
		out any
	}{{settings.SkaelURL, &s.URL}, {settings.SkaelDefaultSet, &s.DefaultSet}, {settings.SkaelAllowedSets, &s.AllowedSets}, {settings.SkaelReviewSkill, &s.ReviewSkill}, {settings.SkaelActivationHook, &s.Hook}} {
		if err := a.Settings.Decode(ctx, kv.key, kv.out); err != nil {
			return b, err
		}
	}
	for name, dst := range map[string]*secrets.Field{AgentKeySecret: &s.AgentKey, ReporterKeySecret: &s.ReporterKey} {
		id, err := a.Secrets.ID(ctx, name)
		if err == nil {
			if *dst, err = a.Secrets.Describe(ctx, &id); err != nil {
				return b, err
			}
		} else if err != pgx.ErrNoRows {
			return b, err
		}
	}
	if s.AllowedSets == nil {
		s.AllowedSets = []string{}
	}
	return b, nil
}

// Register adds the skills settings operations for admins.
func (a *API) Register(api huma.API, admin map[string]any, actor func(context.Context) audit.Actor) {
	huma.Register(api, huma.Operation{OperationID: "get-skills", Method: http.MethodGet, Path: "/skills", Tags: []string{"skills"}, Metadata: admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body SkillsBody }, error) {
			b, err := a.read(ctx)
			return &struct{ Body SkillsBody }{b}, err
		})
	huma.Register(api, huma.Operation{OperationID: "put-skael", Method: http.MethodPut, Path: "/skills/skael", Tags: []string{"skills"}, Metadata: admin},
		func(ctx context.Context, in *struct{ Body SkaelInput }) (*struct{ Body SkillsBody }, error) {
			v := in.Body
			if v.URL != "" {
				if u, err := url.Parse(v.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					return nil, huma.Error422UnprocessableEntity("url: give an http or https URL")
				}
			}
			if v.ReviewSkill == "" {
				v.ReviewSkill = "pr-review"
			}
			if v.AllowedSets == nil {
				v.AllowedSets = []string{}
			}
			who := actor(ctx)
			for key, val := range map[string]any{settings.SkaelURL: v.URL, settings.SkaelDefaultSet: v.DefaultSet, settings.SkaelAllowedSets: v.AllowedSets,
				settings.SkaelReviewSkill: v.ReviewSkill, settings.SkaelActivationHook: v.Hook} {
				raw, _ := json.Marshal(val)
				if _, err := a.Settings.Set(ctx, who, key, raw); err != nil {
					return nil, err
				}
			}
			for name, inp := range map[string]*secrets.Input{AgentKeySecret: v.AgentKey, ReporterKeySecret: v.ReporterKey} {
				if _, err := a.Secrets.Apply(ctx, who, name, inp, nil); err != nil {
					return nil, huma.Error422UnprocessableEntity(err.Error())
				}
			}
			b, err := a.read(ctx)
			return &struct{ Body SkillsBody }{b}, err
		})
}
