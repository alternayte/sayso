package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/secrets"
)

// Access is the metadata and actor of the operations.
type Access struct {
	Admin map[string]any
	Actor func(context.Context) audit.Actor
}

type ServerInput struct {
	Name         string            `json:"name"`
	URL          string            `json:"url" doc:"Streamable HTTP URL"`
	Headers      map[string]string `json:"headers,omitempty"`
	SecretHeader string            `json:"secret_header,omitempty" doc:"Header that carries the secret value"`
	Secret       *secrets.Input    `json:"secret,omitempty"`
	AllowTools   []string          `json:"allow_tools,omitempty" doc:"Name patterns of write tools to allow; needs allow_write"`
	AllowWrite   bool              `json:"allow_write,omitempty"`
}

type ServerBody struct {
	Name         string            `json:"name"`
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers"`
	SecretHeader string            `json:"secret_header"`
	Secret       secrets.Field     `json:"secret"`
	AllowTools   []string          `json:"allow_tools"`
	AllowWrite   bool              `json:"allow_write"`
	Tools        []ToolInfo        `json:"tools"`
}

func (s *Service) body(ctx context.Context, m McpServer) (ServerBody, error) {
	b := ServerBody{Name: m.Name, URL: m.Url, Headers: map[string]string{}, SecretHeader: m.SecretHeader, AllowTools: m.AllowTools, AllowWrite: m.AllowWrite, Tools: []ToolInfo{}}
	_ = json.Unmarshal(m.Headers, &b.Headers)
	if len(m.ToolsCache) > 0 {
		_ = json.Unmarshal(m.ToolsCache, &b.Tools)
	}
	if b.AllowTools == nil {
		b.AllowTools = []string{}
	}
	f, err := s.secrets.Describe(ctx, m.SecretID)
	b.Secret = f
	return b, err
}

func (in ServerInput) check() error {
	if !serverName.MatchString(in.Name) {
		return errors.New("name: use lower-case letters, digits and hyphens")
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url: give an http or https URL")
	}
	if len(in.AllowTools) > 0 && !in.AllowWrite {
		return errors.New("allow_tools needs allow_write: patterns only add tools without the read-only hint")
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return huma.Error404NotFound("MCP server not found")
	}
	return err
}

// Register adds the MCP server operations for admins (REQ-MCP-001).
func (s *Service) Register(api huma.API, acc Access) {
	tags := []string{"mcp"}
	huma.Register(api, huma.Operation{OperationID: "list-mcp-servers", Method: http.MethodGet, Path: "/mcp-servers", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []ServerBody }, error) {
			rows, err := New(s.pool).List(ctx)
			if err != nil {
				return nil, err
			}
			out := []ServerBody{}
			for _, r := range rows {
				b, err := s.body(ctx, r)
				if err != nil {
					return nil, err
				}
				out = append(out, b)
			}
			return &struct{ Body []ServerBody }{out}, nil
		})

	save := func(ctx context.Context, in ServerInput, create bool) (*struct{ Body ServerBody }, error) {
		if err := in.check(); err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		q := New(s.pool)
		cur, err := q.Get(ctx, in.Name)
		switch {
		case create && err == nil:
			return nil, huma.Error409Conflict("an MCP server with this name exists")
		case !create && err != nil:
			return nil, notFound(err)
		}
		actor := acc.Actor(ctx)
		sid, err := s.secrets.Apply(ctx, actor, "mcp."+in.Name+".secret", in.Secret, cur.SecretID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		if in.Headers == nil {
			in.Headers = map[string]string{}
		}
		if in.AllowTools == nil {
			in.AllowTools = []string{}
		}
		h, _ := json.Marshal(in.Headers)
		var row McpServer
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if create {
				row, err = New(tx).Insert(ctx, InsertParams{Name: in.Name, Url: in.URL, Headers: h, SecretHeader: in.SecretHeader, SecretID: sid, AllowTools: in.AllowTools, AllowWrite: in.AllowWrite})
			} else {
				row, err = New(tx).Update(ctx, UpdateParams{Name: in.Name, Url: in.URL, Headers: h, SecretHeader: in.SecretHeader, SecretID: sid, AllowTools: in.AllowTools, AllowWrite: in.AllowWrite})
			}
			if err != nil {
				return err
			}
			action := "mcp.update"
			if create {
				action = "mcp.create"
			}
			return audit.Write(ctx, tx, actor, action, in.Name, map[string]any{"url": in.URL, "allow_write": in.AllowWrite, "allow_tools": in.AllowTools})
		})
		if err != nil {
			return nil, err
		}
		b, err := s.body(ctx, row)
		return &struct{ Body ServerBody }{b}, err
	}

	huma.Register(api, huma.Operation{OperationID: "create-mcp-server", Method: http.MethodPost, Path: "/mcp-servers", Tags: tags, Metadata: acc.Admin, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *struct{ Body ServerInput }) (*struct{ Body ServerBody }, error) {
			return save(ctx, in.Body, true)
		})
	huma.Register(api, huma.Operation{OperationID: "update-mcp-server", Method: http.MethodPut, Path: "/mcp-servers/{name}", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
			Body ServerInput
		}) (*struct{ Body ServerBody }, error) {
			in.Body.Name = in.Name
			return save(ctx, in.Body, false)
		})
	huma.Register(api, huma.Operation{OperationID: "delete-mcp-server", Method: http.MethodDelete, Path: "/mcp-servers/{name}", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{}, error) {
			cur, err := New(s.pool).Get(ctx, in.Name)
			if err != nil {
				return nil, notFound(err)
			}
			actor := acc.Actor(ctx)
			err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE repositories SET mcp_servers = array_remove(mcp_servers, $1)`, in.Name); err != nil {
					return err
				}
				if err := New(tx).DeleteServer(ctx, in.Name); err != nil {
					return err
				}
				return audit.Write(ctx, tx, actor, "mcp.delete", in.Name, nil)
			})
			if err == nil && cur.SecretID != nil {
				err = s.secrets.Remove(ctx, actor, "mcp."+in.Name+".secret")
			}
			return nil, err
		})
	huma.Register(api, huma.Operation{OperationID: "test-mcp-server", Method: http.MethodPost, Path: "/mcp-servers/{name}/test", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{ Body []ToolInfo }, error) {
			tools, err := s.Test(ctx, in.Name)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound(err)
			}
			if err != nil {
				return nil, huma.Error502BadGateway(err.Error())
			}
			return &struct{ Body []ToolInfo }{tools}, nil
		})
}

// Names returns the configured server names.
func (s *Service) Names(ctx context.Context) ([]string, error) {
	rows, err := New(s.pool).List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out, nil
}

// Check lists the tools of a server for the diagnostics.
func (s *Service) Check(ctx context.Context, name string) (string, error) {
	tools, err := s.Test(ctx, name)
	if err != nil {
		return "", err
	}
	allowed := 0
	for _, t := range tools {
		if t.Allowed {
			allowed++
		}
	}
	return fmt.Sprintf("%d tools, %d allowed", len(tools), allowed), nil
}
