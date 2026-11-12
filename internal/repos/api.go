// Package repos holds the repositories that the App can see, their modes
// and their per-repository settings.
package repos

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
)

// Modes in the order of §10.1.
var Modes = []string{"off", "shadow", "comment", "gate"}

type RepositoryBody struct {
	ID            string     `json:"id"`
	FullName      string     `json:"full_name"`
	DefaultBranch string     `json:"default_branch"`
	Private       bool       `json:"private"`
	Mode          string     `json:"mode" enum:"off,shadow,comment,gate"`
	EnabledAt     *time.Time `json:"enabled_at"`
}

func body(r Repository) RepositoryBody {
	return RepositoryBody{ID: r.ID.String(), FullName: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private, Mode: r.Mode, EnabledAt: r.EnabledAt}
}

// Access is the operation metadata of each access level. The caller owns
// the role model.
type Access struct {
	View, Edit map[string]any
	Actor      func(context.Context) audit.Actor
}

type repoPath struct {
	Owner string `path:"owner"`
	Name  string `path:"name"`
}

func (p repoPath) full() string { return p.Owner + "/" + p.Name }

// Register adds the repository operations.
func Register(api huma.API, pool *pgxpool.Pool, acc Access) {
	huma.Register(api, huma.Operation{
		OperationID: "list-repositories", Method: http.MethodGet, Path: "/repositories", Tags: []string{"repositories"}, Metadata: acc.View,
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body []RepositoryBody }, error) {
		rows, err := New(pool).List(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]RepositoryBody, 0, len(rows))
		for _, r := range rows {
			out = append(out, body(r))
		}
		return &struct{ Body []RepositoryBody }{out}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-repository", Method: http.MethodGet, Path: "/repositories/{owner}/{name}", Tags: []string{"repositories"}, Metadata: acc.View,
	}, func(ctx context.Context, in *repoPath) (*struct{ Body RepositoryBody }, error) {
		r, err := New(pool).ByFullName(ctx, in.full())
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("repository not found")
		}
		if err != nil {
			return nil, err
		}
		return &struct{ Body RepositoryBody }{body(r)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "set-repository-mode", Method: http.MethodPut, Path: "/repositories/{owner}/{name}/mode", Tags: []string{"repositories"}, Metadata: acc.Edit,
	}, func(ctx context.Context, in *struct {
		Owner string `path:"owner"`
		Name  string `path:"name"`
		Body  struct {
			Mode string `json:"mode" enum:"off,shadow,comment,gate"`
		}
	}) (*struct{ Body RepositoryBody }, error) {
		var out Repository
		full := in.Owner + "/" + in.Name
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			q := New(tx)
			r, err := q.ByFullName(ctx, full)
			if err != nil {
				return err
			}
			if out, err = q.SetMode(ctx, SetModeParams{ID: r.ID, Mode: in.Body.Mode}); err != nil {
				return err
			}
			return audit.Write(ctx, tx, acc.Actor(ctx), "repository.mode", r.FullName, map[string]any{"old": r.Mode, "new": in.Body.Mode})
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("repository not found")
		}
		if err != nil {
			return nil, err
		}
		return &struct{ Body RepositoryBody }{body(out)}, nil
	})
}
