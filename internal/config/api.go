package config

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alternayte/sayso/internal/audit"
)

type YAMLBody struct {
	YAML string `json:"yaml" maxLength:"2000000"`
}

type PlanBody struct {
	Changes []ConfigChange `json:"changes"`
}

type ApplyInput struct {
	YAML string `json:"yaml" maxLength:"2000000"`
	// Changes is the diff that the admin confirmed.
	Changes []ConfigChange `json:"changes"`
}

// Register adds export, plan and apply (Settings → Config, REQ-CFG-005).
func (s *Service) Register(api huma.API, admin map[string]any, actor func(context.Context) audit.Actor) {
	tags := []string{"config"}
	huma.Register(api, huma.Operation{OperationID: "export-config", Method: http.MethodGet, Path: "/config/export", Tags: tags, Metadata: admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body YAMLBody }, error) {
			b, err := s.Export(ctx)
			return &struct{ Body YAMLBody }{YAMLBody{string(b)}}, err
		})
	huma.Register(api, huma.Operation{OperationID: "plan-config", Method: http.MethodPost, Path: "/config/plan", Tags: tags, Metadata: admin},
		func(ctx context.Context, in *struct{ Body YAMLBody }) (*struct{ Body PlanBody }, error) {
			c, err := s.Plan(ctx, []byte(in.Body.YAML))
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			return &struct{ Body PlanBody }{PlanBody{c}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "apply-config", Method: http.MethodPost, Path: "/config/apply", Tags: tags, Metadata: admin},
		func(ctx context.Context, in *struct{ Body ApplyInput }) (*struct{ Body PlanBody }, error) {
			expect := in.Body.Changes
			if expect == nil {
				expect = []ConfigChange{}
			}
			c, err := s.Apply(ctx, actor(ctx), []byte(in.Body.YAML), expect)
			if errors.Is(err, ErrChanged) {
				return nil, huma.Error409Conflict(err.Error())
			}
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			return &struct{ Body PlanBody }{PlanBody{c}}, nil
		})
}
