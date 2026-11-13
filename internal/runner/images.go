package runner

import (
	"context"
	"net/http"
	"regexp"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
)

// ImageBody is one agent image that repositories can choose (§8.10).
type ImageBody struct {
	Name    string `json:"name"`
	Image   string `json:"image" doc:"Image reference with a tag or digest"`
	Default bool   `json:"default"`
}

var imageName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// RegisterImages adds the agent image operations for admins.
func RegisterImages(api huma.API, pool *pgxpool.Pool, admin map[string]any, actor func(context.Context) audit.Actor) {
	tags := []string{"images"}
	huma.Register(api, huma.Operation{OperationID: "list-agent-images", Method: http.MethodGet, Path: "/agent-images", Tags: tags, Metadata: admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []ImageBody }, error) {
			rows, err := pool.Query(ctx, `SELECT name, image, is_default FROM agent_images ORDER BY name`)
			if err != nil {
				return nil, err
			}
			out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ImageBody])
			if out == nil {
				out = []ImageBody{}
			}
			return &struct{ Body []ImageBody }{out}, err
		})
	huma.Register(api, huma.Operation{OperationID: "put-agent-image", Method: http.MethodPut, Path: "/agent-images/{name}", Tags: tags, Metadata: admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
			Body struct {
				Image   string `json:"image" minLength:"3"`
				Default bool   `json:"default,omitempty"`
			}
		}) (*struct{ Body ImageBody }, error) {
			if !imageName.MatchString(in.Name) {
				return nil, huma.Error422UnprocessableEntity("name: use lower-case letters, digits and hyphens")
			}
			err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				if in.Body.Default {
					if _, err := tx.Exec(ctx, `UPDATE agent_images SET is_default = false WHERE is_default`); err != nil {
						return err
					}
				}
				if _, err := tx.Exec(ctx, `INSERT INTO agent_images (name, image, is_default) VALUES ($1, $2, $3)
					ON CONFLICT (name) DO UPDATE SET image = EXCLUDED.image, is_default = EXCLUDED.is_default`, in.Name, in.Body.Image, in.Body.Default); err != nil {
					return err
				}
				return audit.Write(ctx, tx, actor(ctx), "agent_image.put", in.Name, map[string]any{"image": in.Body.Image, "default": in.Body.Default})
			})
			return &struct{ Body ImageBody }{ImageBody{Name: in.Name, Image: in.Body.Image, Default: in.Body.Default}}, err
		})
	huma.Register(api, huma.Operation{OperationID: "delete-agent-image", Method: http.MethodDelete, Path: "/agent-images/{name}", Tags: tags, Metadata: admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{}, error) {
			return nil, pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `DELETE FROM agent_images WHERE name = $1`, in.Name); err != nil {
					return err
				}
				return audit.Write(ctx, tx, actor(ctx), "agent_image.delete", in.Name, nil)
			})
		})
}
