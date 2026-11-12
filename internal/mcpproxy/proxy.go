package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alternayte/sayso/internal/runner"
)

// Proxy serves /mcp/{name} for agent runs (REQ-MCP-003). It accepts only an
// active run token, and only a server that the run and its repository may
// use. tools/list omits blocked tools, tools/call refuses them, and each
// call writes an mcp_call event.
type Proxy struct {
	svc    *Service
	redact func(string) string
	now    func() time.Time

	mu       sync.Mutex
	handlers map[string]*mcp.StreamableHTTPHandler // run/server
	upstream map[string]*mcp.ClientSession
}

func (s *Service) NewProxy(redact func(string) string) *Proxy {
	p := &Proxy{svc: s, redact: redact, now: time.Now, handlers: map[string]*mcp.StreamableHTTPHandler{}, upstream: map[string]*mcp.ClientSession{}}
	return p
}

type proxyRun struct{}

func deny(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": -32001, "message": msg}})
}

// runServers returns the MCP servers of a run's spec that its repository allows.
func (p *Proxy) runServers(ctx context.Context, run runner.AgentRun) ([]string, error) {
	var spec runner.Spec
	if err := json.Unmarshal(run.Spec, &spec); err != nil {
		return nil, err
	}
	var names []string
	for _, m := range spec.MCP {
		names = append(names, m.Name)
	}
	if run.RepositoryID == nil {
		return names, nil
	}
	repo, err := New(p.svc.pool).RepositoryServers(ctx, *run.RepositoryID)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(names, func(n string) bool { return !slices.Contains(repo, n) }), nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	run, err := runner.Active(ctx, p.svc.pool, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), p.now())
	if err != nil || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		deny(w, http.StatusUnauthorized, "the run token is not valid")
		return
	}
	name := chi.URLParam(r, "name")
	allowed, err := p.runServers(ctx, run)
	if err != nil {
		deny(w, http.StatusInternalServerError, "run servers not available")
		return
	}
	if !slices.Contains(allowed, name) {
		deny(w, http.StatusForbidden, fmt.Sprintf("MCP server %s is not allowed for this run", name))
		return
	}
	srv, err := New(p.svc.pool).Get(ctx, name)
	if err != nil {
		deny(w, http.StatusNotFound, "unknown MCP server")
		return
	}
	key := run.ID.String() + "/" + name
	p.mu.Lock()
	h := p.handlers[key]
	if h == nil {
		// Every request carries a run token, so the loopback Host check against
		// DNS rebinding does not add protection; agent containers use other host names.
		h = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return p.server(run.ID, srv) },
			&mcp.StreamableHTTPOptions{Logger: p.svc.log, DisableLocalhostProtection: true})
		p.handlers[key] = h
	}
	p.mu.Unlock()
	// The agent's credential stays here; upstream calls use the stored headers.
	r.Header.Del("Authorization")
	h.ServeHTTP(w, r.WithContext(context.WithValue(ctx, proxyRun{}, run)))
}

// server builds the MCP server that the agent sees for one run and server:
// the allowed upstream tools, each forwarded to the upstream session.
func (p *Proxy) server(runID uuid.UUID, srv McpServer) *mcp.Server {
	out := mcp.NewServer(&mcp.Implementation{Name: "sayso-mcp-proxy", Version: "1"}, &mcp.ServerOptions{Logger: p.svc.log})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cs, err := p.session(ctx, runID, srv)
	if err != nil {
		p.event(runID, srv.Name, "connect", "error", 0, p.redact(err.Error()))
		return out
	}
	tools, err := listTools(ctx, cs)
	if err != nil {
		p.event(runID, srv.Name, "tools/list", "error", 0, p.redact(err.Error()))
		return out
	}
	for _, t := range tools {
		if !Allowed(t, srv.AllowWrite, srv.AllowTools) {
			continue
		}
		name := t.Name
		out.AddTool(t, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: req.Params.Arguments})
			status, preview := "ok", ""
			if err != nil {
				status, preview = "error", err.Error()
			} else {
				if res.IsError {
					status = "tool_error"
				}
				b, _ := json.Marshal(res.Content)
				preview = string(b)
			}
			p.event(runID, srv.Name, name, status, time.Since(start), p.redact(clip(string(mustJSON(req.Params.Arguments))+" → "+preview, 500)))
			return res, err
		})
	}
	return out
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// session returns the upstream session of a run and server, opened once.
func (p *Proxy) session(ctx context.Context, runID uuid.UUID, srv McpServer) (*mcp.ClientSession, error) {
	key := runID.String() + "/" + srv.Name
	p.mu.Lock()
	cs := p.upstream[key]
	p.mu.Unlock()
	if cs != nil {
		return cs, nil
	}
	cs, err := p.svc.connect(ctx, srv)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.upstream[key] = cs
	p.mu.Unlock()
	return cs, nil
}

// Sweep closes the sessions of runs that ended. The API calls it every minute.
func (p *Proxy) Sweep(ctx context.Context) {
	p.mu.Lock()
	keys := make([]string, 0, len(p.handlers))
	for k := range p.handlers {
		keys = append(keys, k)
	}
	p.mu.Unlock()
	for _, k := range keys {
		id, err := uuid.Parse(strings.SplitN(k, "/", 2)[0])
		if err != nil {
			continue
		}
		run, err := runner.New(p.svc.pool).GetRun(ctx, id)
		if err == nil && run.Status == "running" {
			continue
		}
		p.mu.Lock()
		if cs := p.upstream[k]; cs != nil {
			_ = cs.Close()
		}
		delete(p.upstream, k)
		delete(p.handlers, k)
		p.mu.Unlock()
	}
}

func (p *Proxy) event(runID uuid.UUID, server, tool, status string, d time.Duration, preview string) {
	err := runner.New(p.svc.pool).InsertServerEvent(context.Background(), runner.InsertServerEventParams{
		RunID: runID, Type: "mcp_call", Name: server + "/" + tool, Status: status, DurationMs: int32(d.Milliseconds()), Preview: preview,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		p.svc.log.Error("mcp event not stored", "error", err)
	}
}

// Close closes every upstream session, at shutdown.
func (p *Proxy) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, cs := range p.upstream {
		_ = cs.Close()
		delete(p.upstream, k)
	}
	clear(p.handlers)
}
