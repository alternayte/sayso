// Package mcp is a fake MCP server for tests and the Compose "fakes"
// profile. It has tools with and without the read-only hint and records the
// headers of each request.
package mcp

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Fake is a running fake MCP server.
type Fake struct {
	mu      sync.Mutex
	headers []http.Header
	calls   []string
}

func New() *Fake { return &Fake{} }

type query struct {
	Query string `json:"query" jsonschema:"what to look for"`
}

type ticket struct {
	Title string `json:"title"`
}

func (f *Fake) server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "acme-catalog", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "catalog_search", Description: "Search the acme service catalog", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, _ *mcp.CallToolRequest, in query) (*mcp.CallToolResult, any, error) {
			f.record("catalog_search")
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("payments-api owns %q", in.Query)}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "catalog_owner", Description: "Owner team of a service", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, _ *mcp.CallToolRequest, in query) (*mcp.CallToolResult, any, error) {
			f.record("catalog_owner")
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "team payments"}}}, nil, nil
		})
	yes := true
	mcp.AddTool(s, &mcp.Tool{Name: "ticket_create", Description: "Create a ticket", Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes}},
		func(_ context.Context, _ *mcp.CallToolRequest, in ticket) (*mcp.CallToolResult, any, error) {
			f.record("ticket_create")
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "created " + in.Title}}}, nil, nil
		})
	return s
}

func (f *Fake) record(call string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}

// Handler serves Streamable HTTP and records the request headers.
func (f *Fake) Handler() http.Handler {
	s := f.server()
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()
		h.ServeHTTP(w, r)
	})
}

// Headers returns the recorded request headers.
func (f *Fake) Headers() []http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]http.Header(nil), f.headers...)
}

// Calls returns the names of the tools that were called.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
