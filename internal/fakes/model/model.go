// Package model is a fake model endpoint for tests and the Compose "fakes"
// profile. It speaks openai-completions, openai-responses and
// anthropic-messages, with streaming, tool calls and usage, and fails on
// demand. It records every request.
package model

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Failure modes for Fail.
const (
	Fail429     = "429"
	Fail500     = "500"
	FailCut     = "cut"     // send the first stream event, then close the connection
	FailNoTools = "notools" // answer text even when tools are present
)

// NoToolsPrefix makes every request for a model with this prefix answer
// without tool calls.
const NoToolsPrefix = "no-tools-"

// Request is one recorded call.
type Request struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
	At     time.Time
}

// Fake is a running fake model endpoint.
type Fake struct {
	mu       sync.Mutex
	requests []Request
	fails    []string
	// Delay is waited before each answer, for concurrency tests.
	Delay time.Duration
	// Models lists the IDs that GET /models returns.
	Models []string
}

// New returns a fake with three listed models.
func New() *Fake {
	return &Fake{Models: []string{"fake-large", "fake-small", NoToolsPrefix + "model"}}
}

// Fail queues failures: each request takes the next one.
func (f *Fake) Fail(mode string, times int) {
	f.mu.Lock()
	for i := 0; i < times; i++ {
		f.fails = append(f.fails, mode)
	}
	f.mu.Unlock()
}

// Requests returns a copy of the recorded requests.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// Reset forgets the recorded requests and queued failures.
func (f *Fake) Reset() {
	f.mu.Lock()
	f.requests, f.fails = nil, nil
	f.mu.Unlock()
}

// Handler serves the three API types. The base URL may end with /v1 or not.
func (f *Fake) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, prefix := range []string{"", "/v1"} {
		mux.HandleFunc("POST "+prefix+"/chat/completions", f.completions)
		mux.HandleFunc("POST "+prefix+"/responses", f.responses)
		mux.HandleFunc("POST "+prefix+"/messages", f.messages)
		mux.HandleFunc("GET "+prefix+"/models", f.models)
	}
	return f.record(mux)
}

func (f *Fake) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		f.mu.Lock()
		f.requests = append(f.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: string(body), At: time.Now()})
		delay := f.Delay
		f.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		next.ServeHTTP(w, r)
	})
}

func (f *Fake) nextFailure() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.fails) == 0 {
		return ""
	}
	m := f.fails[0]
	f.fails = f.fails[1:]
	return m
}

// failed writes a 429 or 500 and reports whether it did.
func failed(w http.ResponseWriter, mode string) bool {
	switch mode {
	case Fail429:
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"type": "rate_limit_error", "message": "rate limited"}})
		return true
	case Fail500:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"type": "server_error", "message": "upstream failed"}})
		return true
	}
	return false
}

func (f *Fake) models(w http.ResponseWriter, _ *http.Request) {
	var data []map[string]any
	for _, m := range f.Models {
		data = append(data, map[string]any{"id": m, "object": "model"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// reply is the answer the fake chose for one request.
type reply struct {
	Text     string
	ToolName string
	ToolArgs string
	In, Out  int
}

// decide picks the answer: a tool call when tools are present and no tool
// result came back yet, else text. A prompt that asks for JSON gets JSON.
func decide(model string, tools []string, toolResult string, prompt string, bodyLen int, noTools bool) reply {
	r := reply{In: bodyLen/4 + 1}
	switch {
	case len(tools) > 0 && toolResult == "" && !noTools && !strings.HasPrefix(model, NoToolsPrefix):
		r.ToolName, r.ToolArgs = tools[0], `{"city":"Oslo"}`
	case toolResult != "":
		r.Text = "The tool returned: " + toolResult
	case strings.Contains(strings.ToLower(prompt), "json"):
		r.Text = `{"answer":"ok"}`
	default:
		r.Text = "Hello from the fake model."
	}
	r.Out = (len(r.Text)+len(r.ToolArgs))/4 + 1
	return r
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// sse writes server-sent events. cut closes the connection after the first.
type sse struct {
	w     http.ResponseWriter
	fl    http.Flusher
	cut   bool
	count int
	dead  bool
}

func newSSE(w http.ResponseWriter, cut bool) *sse {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	return &sse{w: w, fl: fl, cut: cut}
}

func (s *sse) send(event string, v any) {
	if s.dead {
		return
	}
	if s.cut && s.count == 1 {
		s.dead = true
		if hj, ok := s.w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
		return
	}
	b, _ := json.Marshal(v)
	if event != "" {
		_, _ = fmt.Fprintf(s.w, "event: %s\n", event)
	}
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", b)
	if s.fl != nil {
		s.fl.Flush()
	}
	s.count++
}

func (s *sse) done() {
	if s.dead {
		return
	}
	_, _ = fmt.Fprint(s.w, "data: [DONE]\n\n")
	if s.fl != nil {
		s.fl.Flush()
	}
}
