package skills

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/secrets"
)

// Extensions holds the pi extensions of agent runs (§14.3).
type Extensions struct {
	Pool    *pgxpool.Pool
	Secrets *secrets.Store
	// Baked lists the package specs that the agent image has installed.
	Baked []string
}

// pinned is a package spec with an exact version: name@1.2.3 or @scope/name@1.2.3.
var pinned = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*@\d+\.\d+\.\d+([-+][0-9A-Za-z.-]+)?$`)

var extName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type ExtensionInput struct {
	Spec         string                    `json:"spec" doc:"npm package with an exact version, for example acme-pi-lint@1.4.0"`
	Env          map[string]string         `json:"env,omitempty"`
	Secrets      map[string]*secrets.Input `json:"secrets,omitempty" doc:"Environment variables with secret values"`
	Enabled      bool                      `json:"enabled"`
	Repositories []string                  `json:"repositories,omitempty" doc:"Empty: every repository"`
}

type ExtensionBody struct {
	Name         string                   `json:"name"`
	Kind         string                   `json:"kind"`
	Spec         string                   `json:"spec"`
	Env          map[string]string        `json:"env"`
	Secrets      map[string]secrets.Field `json:"secrets"`
	Enabled      bool                     `json:"enabled"`
	Repositories []string                 `json:"repositories"`
	Baked        bool                     `json:"baked" doc:"False: the run installs it from the registry at start and caches it nowhere"`
}

func (x *Extensions) body(ctx context.Context, name, kind, spec string, env, ids []byte, enabled bool, repos []string) (ExtensionBody, error) {
	b := ExtensionBody{Name: name, Kind: kind, Spec: spec, Env: map[string]string{}, Secrets: map[string]secrets.Field{}, Enabled: enabled, Repositories: repos,
		Baked: slices.Contains(x.Baked, spec)}
	if b.Repositories == nil {
		b.Repositories = []string{}
	}
	_ = json.Unmarshal(env, &b.Env)
	var m map[string]uuid.UUID
	_ = json.Unmarshal(ids, &m)
	for k, id := range m {
		f, err := x.Secrets.Describe(ctx, &id)
		if err != nil {
			return b, err
		}
		b.Secrets[k] = f
	}
	return b, nil
}

// Register adds the extension operations for admins.
func (x *Extensions) Register(api huma.API, admin map[string]any, actor func(context.Context) audit.Actor) {
	tags := []string{"extensions"}
	huma.Register(api, huma.Operation{OperationID: "list-extensions", Method: http.MethodGet, Path: "/extensions", Tags: tags, Metadata: admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []ExtensionBody }, error) {
			rows, err := x.Pool.Query(ctx, `SELECT name, kind, spec, env, secret_ids, enabled, repositories FROM extensions ORDER BY name`)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			out := []ExtensionBody{}
			for rows.Next() {
				var name, kind, spec string
				var env, ids []byte
				var enabled bool
				var repos []string
				if err := rows.Scan(&name, &kind, &spec, &env, &ids, &enabled, &repos); err != nil {
					return nil, err
				}
				b, err := x.body(ctx, name, kind, spec, env, ids, enabled, repos)
				if err != nil {
					return nil, err
				}
				out = append(out, b)
			}
			return &struct{ Body []ExtensionBody }{out}, rows.Err()
		})

	huma.Register(api, huma.Operation{OperationID: "put-extension", Method: http.MethodPut, Path: "/extensions/{name}", Tags: tags, Metadata: admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
			Body ExtensionInput
		}) (*struct{ Body ExtensionBody }, error) {
			if !extName.MatchString(in.Name) {
				return nil, huma.Error422UnprocessableEntity("name: use lower-case letters, digits and hyphens")
			}
			if !pinned.MatchString(in.Body.Spec) {
				return nil, huma.Error422UnprocessableEntity("spec: give an npm package with an exact version, for example acme-pi-lint@1.4.0")
			}
			a := actor(ctx)
			var cur []byte
			_ = x.Pool.QueryRow(ctx, `SELECT secret_ids FROM extensions WHERE name = $1`, in.Name).Scan(&cur)
			ids := map[string]uuid.UUID{}
			_ = json.Unmarshal(cur, &ids)
			for env, v := range in.Body.Secrets {
				var prev *uuid.UUID
				if id, ok := ids[env]; ok {
					prev = &id
				}
				id, err := x.Secrets.Apply(ctx, a, "extension."+in.Name+"."+strings.ToLower(env), v, prev)
				if err != nil {
					return nil, huma.Error422UnprocessableEntity(err.Error())
				}
				if id != nil {
					ids[env] = *id
				}
			}
			env := in.Body.Env
			if env == nil {
				env = map[string]string{}
			}
			envJSON, _ := json.Marshal(env)
			idJSON, _ := json.Marshal(ids)
			repos := in.Body.Repositories
			if repos == nil {
				repos = []string{}
			}
			err := pgx.BeginFunc(ctx, x.Pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `INSERT INTO extensions (name, kind, spec, env, secret_ids, enabled, repositories) VALUES ($1, 'pi_package', $2, $3, $4, $5, $6)
					ON CONFLICT (name) DO UPDATE SET spec = EXCLUDED.spec, env = EXCLUDED.env, secret_ids = EXCLUDED.secret_ids, enabled = EXCLUDED.enabled,
					repositories = EXCLUDED.repositories, updated_at = now()`, in.Name, in.Body.Spec, envJSON, idJSON, in.Body.Enabled, repos); err != nil {
					return err
				}
				return audit.Write(ctx, tx, a, "extension.put", in.Name, map[string]any{"spec": in.Body.Spec, "enabled": in.Body.Enabled, "repositories": repos})
			})
			if err != nil {
				return nil, err
			}
			b, err := x.body(ctx, in.Name, "pi_package", in.Body.Spec, envJSON, idJSON, in.Body.Enabled, repos)
			return &struct{ Body ExtensionBody }{b}, err
		})

	huma.Register(api, huma.Operation{OperationID: "delete-extension", Method: http.MethodDelete, Path: "/extensions/{name}", Tags: tags, Metadata: admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{}, error) {
			return nil, pgx.BeginFunc(ctx, x.Pool, func(tx pgx.Tx) error {
				tag, err := tx.Exec(ctx, `DELETE FROM extensions WHERE name = $1`, in.Name)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					return huma.Error404NotFound("extension not found")
				}
				return audit.Write(ctx, tx, actor(ctx), "extension.delete", in.Name, nil)
			})
		})
}

// Enabled returns the names of the extensions that are enabled for a repository.
func (x *Extensions) Enabled(ctx context.Context, repoFullName string) ([]string, error) {
	rows, err := x.Pool.Query(ctx, `SELECT name, repositories FROM extensions WHERE enabled AND kind = 'pi_package' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		var repos []string
		if err := rows.Scan(&name, &repos); err != nil {
			return nil, err
		}
		if len(repos) == 0 || slices.ContainsFunc(repos, func(r string) bool { return strings.EqualFold(r, repoFullName) }) {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

// Spec returns the run entries of the named extensions with their
// environment and secret values. Call it with the effective list, which
// holds only extensions enabled for the repository (REQ-SKL-005).
func (x *Extensions) Spec(ctx context.Context, names []string) ([]runner.SpecExtension, error) {
	out := []runner.SpecExtension{}
	for _, n := range names {
		var spec string
		var env, ids []byte
		err := x.Pool.QueryRow(ctx, `SELECT spec, env, secret_ids FROM extensions WHERE name = $1 AND enabled`, n).Scan(&spec, &env, &ids)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		e := runner.SpecExtension{Name: n, Package: spec, Env: map[string]string{}}
		_ = json.Unmarshal(env, &e.Env)
		var m map[string]uuid.UUID
		_ = json.Unmarshal(ids, &m)
		for k, id := range m {
			v, err := x.Secrets.ValueOf(ctx, &id)
			if err != nil {
				return nil, err
			}
			e.Env[k] = v
		}
		out = append(out, e)
	}
	return out, nil
}

// BakedFile lists the extensions that the agent image installed at build time.
const BakedFile = "/opt/sayso/extensions/extensions.lock"

// BakedExtensions reads the baked list; outside the agent image it is empty.
func BakedExtensions() []string {
	b, err := os.ReadFile(BakedFile)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}
