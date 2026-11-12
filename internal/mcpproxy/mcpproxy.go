// Package mcpproxy holds the MCP servers, their tool allowlists and the MCP
// proxy that agent runs reach with their run token (§13, D-06).
package mcpproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alternayte/sayso/internal/secrets"
)

// ToolInfo is a tool of a server as the test stored it.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
	Destructive *bool  `json:"destructive,omitempty"`
	Allowed     bool   `json:"allowed"`
}

// Allowed is the allowlist rule (§13, SI-12): a tool with readOnlyHint true
// is allowed; any other tool needs allow_write on the server and a pattern
// that matches its name.
func Allowed(t *mcp.Tool, allowWrite bool, patterns []string) bool {
	if t.Annotations != nil && t.Annotations.ReadOnlyHint {
		return true
	}
	if !allowWrite {
		return false
	}
	for _, p := range patterns {
		if ok, err := path.Match(p, t.Name); err == nil && ok {
			return true
		}
	}
	return false
}

// Service is the MCP feature.
type Service struct {
	pool    *pgxpool.Pool
	secrets *secrets.Store
	http    *http.Client
	log     *slog.Logger
}

func NewService(pool *pgxpool.Pool, sec *secrets.Store, hc *http.Client, log *slog.Logger) *Service {
	return &Service{pool: pool, secrets: sec, http: hc, log: log}
}

var serverName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// headerTransport adds the stored headers to each upstream request.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
}

// connect opens a client session to a server with its stored headers.
func (s *Service) connect(ctx context.Context, srv McpServer) (*mcp.ClientSession, error) {
	headers := map[string]string{}
	if err := json.Unmarshal(srv.Headers, &headers); err != nil {
		return nil, err
	}
	if srv.SecretID != nil && srv.SecretHeader != "" {
		v, err := s.secrets.ValueOf(ctx, srv.SecretID)
		if err != nil {
			return nil, err
		}
		headers[srv.SecretHeader] = v
	}
	base := s.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	hc := &http.Client{Transport: headerTransport{base: base, headers: headers}}
	c := mcp.NewClient(&mcp.Implementation{Name: "sayso", Version: "1"}, nil)
	return c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.Url, HTTPClient: hc, MaxRetries: -1}, nil)
}

// listTools returns every tool of a server.
func listTools(ctx context.Context, cs *mcp.ClientSession) ([]*mcp.Tool, error) {
	var out []*mcp.Tool
	for t, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Test lists the tools of a server and stores them with their annotations
// (REQ-MCP-001).
func (s *Service) Test(ctx context.Context, name string) ([]ToolInfo, error) {
	srv, err := New(s.pool).Get(ctx, name)
	if err != nil {
		return nil, err
	}
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cs, err := s.connect(tctx, srv)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", srv.Url, err)
	}
	defer cs.Close()
	tools, err := listTools(tctx, cs)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	out := make([]ToolInfo, 0, len(tools))
	for _, t := range tools {
		ti := ToolInfo{Name: t.Name, Description: t.Description, Allowed: Allowed(t, srv.AllowWrite, srv.AllowTools)}
		if t.Annotations != nil {
			ti.ReadOnly, ti.Destructive = t.Annotations.ReadOnlyHint, t.Annotations.DestructiveHint
		}
		out = append(out, ti)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return out, New(s.pool).SetTools(ctx, SetToolsParams{Name: name, ToolsCache: raw})
}

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n]
	}
	return s
}
