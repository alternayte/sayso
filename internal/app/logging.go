package app

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// Redactor replaces known secret values with [redacted:<name>].
// The secrets package and the runner add values as they learn them.
type Redactor struct {
	mu     sync.RWMutex
	values map[string]string // value -> name
}

// NewRedactor returns an empty redactor.
func NewRedactor() *Redactor { return &Redactor{values: map[string]string{}} }

// Add registers a secret value under a name. Values shorter than 6
// characters are ignored, because replacing them would corrupt normal text.
func (r *Redactor) Add(name, value string) {
	if len(value) < 6 {
		return
	}
	r.mu.Lock()
	r.values[value] = name
	r.mu.Unlock()
}

// Remove forgets a secret value.
func (r *Redactor) Remove(value string) {
	r.mu.Lock()
	delete(r.values, value)
	r.mu.Unlock()
}

// Redact returns s with every known secret value replaced.
func (r *Redactor) Redact(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// The longest value first, so a secret that contains another one is
	// replaced whole, the same way each time.
	vals := make([]string, 0, len(r.values))
	for v := range r.values {
		vals = append(vals, v)
	}
	sort.Slice(vals, func(i, j int) bool {
		if len(vals[i]) != len(vals[j]) {
			return len(vals[i]) > len(vals[j])
		}
		return vals[i] < vals[j]
	})
	for _, v := range vals {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, "[redacted:"+r.values[v]+"]")
		}
	}
	return s
}

var sensitiveKeys = map[string]bool{
	"authorization": true, "cookie": true, "set-cookie": true, "password": true,
	"token": true, "secret": true, "api_key": true, "apikey": true, "x-api-key": true, "key": true,
}

// NewLogger returns a JSON logger that redacts known secret values in every
// string and drops the values of sensitive attribute keys.
func NewLogger(w io.Writer, level slog.Level, r *Redactor) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if sensitiveKeys[strings.ToLower(a.Key)] {
				return slog.String(a.Key, "[redacted]")
			}
			switch a.Value.Kind() {
			case slog.KindString:
				return slog.String(a.Key, r.Redact(a.Value.String()))
			case slog.KindAny:
				if err, ok := a.Value.Any().(error); ok {
					return slog.String(a.Key, r.Redact(err.Error()))
				}
			}
			return a
		},
	})
	return slog.New(&redactMsg{Handler: h, r: r})
}

// redactMsg also redacts the log message itself.
type redactMsg struct {
	slog.Handler
	r *Redactor
}

func (h *redactMsg) Handle(ctx context.Context, rec slog.Record) error {
	rec.Message = h.r.Redact(rec.Message)
	return h.Handler.Handle(ctx, rec)
}

func (h *redactMsg) WithAttrs(as []slog.Attr) slog.Handler {
	return &redactMsg{Handler: h.Handler.WithAttrs(as), r: h.r}
}

func (h *redactMsg) WithGroup(name string) slog.Handler {
	return &redactMsg{Handler: h.Handler.WithGroup(name), r: h.r}
}
