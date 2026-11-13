package guidance

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/auth"
	"github.com/alternayte/sayso/internal/runner"
)

// Inputs are the repository parts of a preview: the .sayso.yml
// instructions of the default branch, the execution setting and the text
// of the review skill that a round would use.
type Inputs struct {
	Instructions string
	Execution    bool
	Skill        string
}

type GuidanceBody struct {
	Current *Version  `json:"current,omitempty"`
	History []Version `json:"history"`
}

type PreviewBody struct {
	Repository string    `json:"repository"`
	Stage      string    `json:"stage"`
	Prompt     Assembled `json:"prompt"`
	Versions   struct {
		Global     string `json:"global"`
		Repository string `json:"repository"`
	} `json:"versions"`
}

// API is the guidance editor (Settings → Guidance, the repository page).
type API struct {
	Store *Store
	// Inputs resolves the repository parts of a preview.
	Inputs func(ctx context.Context, repoFullName string) (Inputs, error)
}

func (a *API) repo(ctx context.Context, owner, name string) (uuid.UUID, string, error) {
	var id uuid.UUID
	var full string
	err := a.Store.Pool.QueryRow(ctx, `SELECT id, full_name FROM repositories WHERE lower(full_name) = lower($1) AND removed_at IS NULL`, owner+"/"+name).Scan(&id, &full)
	if errors.Is(err, pgx.ErrNoRows) {
		return id, full, huma.Error404NotFound("repository not found")
	}
	return id, full, err
}

func body(h []Version) GuidanceBody {
	b := GuidanceBody{History: h}
	if len(h) > 0 {
		b.Current = &h[0]
	}
	return b
}

type textInput struct {
	Text string `json:"text" maxLength:"8000"`
}

// Register adds the guidance operations.
func (a *API) Register(api huma.API, view, edit, admin map[string]any, actor func(context.Context) audit.Actor) {
	tags := []string{"guidance"}
	huma.Register(api, huma.Operation{OperationID: "get-guidance", Method: http.MethodGet, Path: "/guidance", Tags: tags, Metadata: admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body GuidanceBody }, error) {
			h, err := a.Store.History(ctx, nil)
			return &struct{ Body GuidanceBody }{body(h)}, err
		})
	huma.Register(api, huma.Operation{OperationID: "put-guidance", Method: http.MethodPut, Path: "/guidance", Tags: tags, Metadata: admin},
		func(ctx context.Context, in *struct{ Body textInput }) (*struct{ Body GuidanceBody }, error) {
			if _, err := a.Store.Save(ctx, actor(ctx), auth.UserFrom(ctx).Login, nil, "global", in.Body.Text); err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			h, err := a.Store.History(ctx, nil)
			return &struct{ Body GuidanceBody }{body(h)}, err
		})
	huma.Register(api, huma.Operation{OperationID: "get-repository-guidance", Method: http.MethodGet, Path: "/repositories/{owner}/{name}/guidance", Tags: tags, Metadata: view},
		func(ctx context.Context, in *struct {
			Owner string `path:"owner"`
			Name  string `path:"name"`
		}) (*struct{ Body GuidanceBody }, error) {
			id, _, err := a.repo(ctx, in.Owner, in.Name)
			if err != nil {
				return nil, err
			}
			h, err := a.Store.History(ctx, &id)
			return &struct{ Body GuidanceBody }{body(h)}, err
		})
	huma.Register(api, huma.Operation{OperationID: "put-repository-guidance", Method: http.MethodPut, Path: "/repositories/{owner}/{name}/guidance", Tags: tags, Metadata: edit},
		func(ctx context.Context, in *struct {
			Owner string `path:"owner"`
			Name  string `path:"name"`
			Body  textInput
		}) (*struct{ Body GuidanceBody }, error) {
			id, full, err := a.repo(ctx, in.Owner, in.Name)
			if err != nil {
				return nil, err
			}
			if _, err := a.Store.Save(ctx, actor(ctx), auth.UserFrom(ctx).Login, &id, full, in.Body.Text); err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			h, err := a.Store.History(ctx, &id)
			return &struct{ Body GuidanceBody }{body(h)}, err
		})
	huma.Register(api, huma.Operation{OperationID: "preview-prompt", Method: http.MethodGet, Path: "/repositories/{owner}/{name}/prompt-preview", Tags: tags, Metadata: edit},
		func(ctx context.Context, in *struct {
			Owner string `path:"owner"`
			Name  string `path:"name"`
			Stage string `query:"stage" enum:"review,challenge,reply,fix" default:"review"`
		}) (*struct{ Body PreviewBody }, error) {
			id, full, err := a.repo(ctx, in.Owner, in.Name)
			if err != nil {
				return nil, err
			}
			g, err := a.Store.Layer(ctx, nil)
			if err != nil {
				return nil, err
			}
			r, err := a.Store.Layer(ctx, &id)
			if err != nil {
				return nil, err
			}
			inp, err := a.Inputs(ctx, full)
			if err != nil {
				return nil, err
			}
			var b PreviewBody
			b.Repository, b.Stage = full, in.Stage
			b.Prompt = Assemble(runner.Prompts{Global: g, Repository: r, File: runner.PromptLayer{Text: inp.Instructions}}, inp.Instructions, inp.Execution, in.Stage, inp.Skill)
			b.Versions.Global, b.Versions.Repository = g.Version, r.Version
			return &struct{ Body PreviewBody }{b}, nil
		})
}
