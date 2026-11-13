package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/alternayte/sayso/internal/runner"
)

// ProxyRetries is how often the proxy retries a 429 or 5xx before the first
// response byte (§12.3).
const ProxyRetries = 2

// ProxyMetrics count proxy calls and errors.
type ProxyMetrics struct {
	Calls *prometheus.CounterVec
}

// NewProxyMetrics registers the model proxy metrics.
func NewProxyMetrics(reg prometheus.Registerer) *ProxyMetrics {
	m := &ProxyMetrics{Calls: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sayso_model_proxy_requests_total", Help: "Model proxy requests by provider and result.",
	}, []string{"provider", "result"})}
	reg.MustRegister(m.Calls)
	return m
}

func proxyError(w http.ResponseWriter, code int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": kind, "message": msg}})
}

// hopHeaders are not forwarded in either direction.
var hopHeaders = []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	"Host", "Content-Length", "Accept-Encoding", "Cookie"}

// modelOf finds the model of a request: in the JSON body, or in the path
// for google-generative-ai (models/<model>:generateContent).
func modelOf(apiType, path string, body []byte) string {
	if apiType == GoogleGenerative {
		rest := strings.TrimPrefix(path, "/")
		if i := strings.Index(rest, "models/"); i >= 0 {
			rest = rest[i+len("models/"):]
			if j := strings.IndexByte(rest, ':'); j >= 0 {
				return rest[:j]
			}
			return rest
		}
		return ""
	}
	var b struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Model
}

// estimate guesses the tokens of a request: the input by size, and the
// largest output it allows.
func estimate(body []byte, target runner.Model) (in, out int64) {
	in = int64(len(body))/4 + 1
	var b struct {
		MaxTokens       int64 `json:"max_tokens"`
		MaxOutputTokens int64 `json:"max_output_tokens"`
		MaxCompletion   int64 `json:"max_completion_tokens"`
		Generation      *struct {
			MaxOutputTokens int64 `json:"maxOutputTokens"`
		} `json:"generationConfig"`
	}
	_ = json.Unmarshal(body, &b)
	out = max(b.MaxTokens, b.MaxOutputTokens, b.MaxCompletion)
	if b.Generation != nil {
		out = max(out, b.Generation.MaxOutputTokens)
	}
	if out == 0 {
		out = int64(target.MaxTokens)
	}
	if out == 0 {
		out = 4096
	}
	return in, out
}

// Proxy serves /llm/{provider}/* for agent runs (§12.3, REQ-MOD-006).
func (s *Service) Proxy(m *ProxyMetrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		start := time.Now()
		name := chi.URLParam(r, "provider")
		path := chi.URLParam(r, "*")
		count := func(result string) {
			if m != nil {
				m.Calls.WithLabelValues(name, result).Inc()
			}
		}
		run, err := runner.Active(ctx, s.pool, runner.TokenFrom(r), s.Now())
		if err != nil {
			count("unauthorized")
			proxyError(w, http.StatusUnauthorized, "authentication_error", "the run token is not valid")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
		if err != nil {
			proxyError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request too large")
			return
		}
		row, err := New(s.pool).GetProvider(ctx, name)
		if err != nil {
			count("unknown_provider")
			proxyError(w, http.StatusNotFound, "not_found_error", "unknown provider")
			return
		}
		c, err := configOf(row)
		if err != nil {
			proxyError(w, http.StatusInternalServerError, "api_error", "provider configuration is not valid")
			return
		}
		model := modelOf(c.APIType, path, body)
		allowed, err := runner.AllowedModels(run)
		if err != nil {
			proxyError(w, http.StatusInternalServerError, "api_error", "run models are not valid")
			return
		}
		var target *runner.Model
		for i := range allowed {
			if allowed[i].Provider == name && allowed[i].Model == model {
				target = &allowed[i]
				break
			}
		}
		if target == nil {
			count("model_refused")
			proxyError(w, http.StatusForbidden, "permission_error", fmt.Sprintf("model %q of provider %s is not in the profiles of this run", model, name))
			return
		}
		estIn, estOut := estimate(body, *target)
		if run.BudgetUsd > 0 {
			spent, err := New(s.pool).RunSpend(ctx, run.ID)
			if err != nil {
				proxyError(w, http.StatusInternalServerError, "api_error", "usage not available")
				return
			}
			est := Usage{In: estIn, Out: estOut}.Cost(target.PriceIn, target.PriceOut, target.PriceCacheRead)
			if spent+est > run.BudgetUsd {
				count("budget_exceeded")
				s.event(ctx, run.ID, "error", "budget_exceeded", "refused", 0, 0, 0,
					fmt.Sprintf("Round budget %.4f USD: spent %.4f, this request could cost %.4f.", run.BudgetUsd, spent, est))
				proxyError(w, http.StatusPaymentRequired, "budget_exceeded", "the request would pass the round budget")
				return
			}
		}
		g, err := s.limits.acquire(ctx, c, estIn+estOut)
		if err != nil {
			count("no_capacity")
			w.Header().Set("Retry-After", "5")
			proxyError(w, http.StatusTooManyRequests, "rate_limit_error", "the provider limits stayed full for 60 s")
			return
		}
		defer g.Release()
		key, err := s.Key(ctx, c)
		if err != nil {
			proxyError(w, http.StatusBadGateway, "api_error", "the provider key is not available")
			return
		}
		q := r.URL.Query()
		q.Del("key")
		upstream, err := c.Endpoint(path, q.Encode())
		if err != nil {
			proxyError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		resp, err := s.send(ctx, r, c, key, upstream, body)
		if err != nil {
			count("upstream_error")
			s.event(ctx, run.ID, "model_turn", model, "error", time.Since(start), 0, 0, s.redact(err.Error()))
			proxyError(w, http.StatusBadGateway, "api_error", "the provider is not reachable")
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			if !isHop(k) {
				w.Header()[k] = vs
			}
		}
		w.WriteHeader(resp.StatusCode)
		ur := &usageReader{stream: strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")}
		copyErr := stream(w, io.TeeReader(resp.Body, ur))
		u := ur.Result()
		g.Settle(u.In + u.Out + u.CacheRead)
		cost := u.Cost(target.PriceIn, target.PriceOut, target.PriceCacheRead)
		latency := time.Since(start)
		// The usage row survives a client that went away.
		bg := context.WithoutCancel(ctx)
		if err := New(s.pool).InsertUsage(bg, InsertUsageParams{RunID: run.ID, Provider: name, Model: model, TokensIn: u.In, TokensOut: u.Out,
			TokensCacheRead: u.CacheRead, CostUsd: cost, Status: int32(resp.StatusCode), LatencyMs: int32(latency.Milliseconds())}); err != nil {
			s.log.Error("model usage not stored", "error", err)
		}
		if err := New(s.pool).AddRunUsage(bg, AddRunUsageParams{ID: run.ID, TokensIn: u.In, TokensOut: u.Out, CostUsd: cost}); err != nil {
			s.log.Error("run usage not stored", "error", err)
		}
		status := strconv.Itoa(resp.StatusCode)
		if copyErr != nil {
			status = "stream_error"
		}
		s.event(bg, run.ID, "model_turn", model, status, latency, u.In, u.Out, fmt.Sprintf("%s %s: %d in, %d out, %.6f USD", name, model, u.In, u.Out, cost))
		count(status)
		if copyErr != nil {
			// The client must see a broken stream, not a clean end.
			panic(http.ErrAbortHandler)
		}
	}
}

func isHop(k string) bool {
	for _, h := range hopHeaders {
		if strings.EqualFold(k, h) {
			return true
		}
	}
	return false
}

// send forwards the request, and retries 429, 5xx and connection errors
// up to ProxyRetries times. Nothing reached the client yet, so a retry is safe.
func (s *Service) send(ctx context.Context, in *http.Request, c Config, key, upstream string, body []byte) (*http.Response, error) {
	var last error
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, in.Method, upstream, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for k, vs := range in.Header {
			if isHop(k) || isCredential(k) {
				continue
			}
			req.Header[k] = vs
		}
		c.SetCredential(req.Header, key)
		resp, err := s.streamClient().Do(req)
		retry := err != nil || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if !retry || attempt == ProxyRetries {
			if err != nil {
				return nil, err
			}
			return resp, nil
		}
		wait := time.Duration(250<<attempt) * time.Millisecond
		if err != nil {
			last = err
		} else {
			if ra, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && ra > 0 && ra <= 10 {
				wait = time.Duration(ra) * time.Second
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), last)
		case <-time.After(wait):
		}
	}
}

// streamClient is the outbound client without its overall timeout, so long
// model streams are not cut.
func (s *Service) streamClient() *http.Client {
	c := *s.http
	c.Timeout = 0
	return &c
}

func isCredential(k string) bool {
	for _, h := range credentialHeaders {
		if strings.EqualFold(k, h) {
			return true
		}
	}
	return false
}

// stream copies the body and flushes after each read, so events reach the
// agent at once.
func stream(w http.ResponseWriter, r io.Reader) error {
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (s *Service) event(ctx context.Context, run [16]byte, typ, name, status string, d time.Duration, in, out int64, preview string) {
	if len(preview) > 500 {
		preview = preview[:500]
	}
	err := runner.New(s.pool).InsertServerEvent(ctx, runner.InsertServerEventParams{
		RunID: run, Type: typ, Name: name, Status: status, DurationMs: int32(d.Milliseconds()), TokensIn: in, TokensOut: out, Preview: s.redact(preview),
	})
	if err != nil {
		s.log.Error("run event not stored", "error", err)
	}
}
