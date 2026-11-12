package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// httpMetrics records request counts and durations by route pattern.
func httpMetrics(reg *prometheus.Registry) func(http.Handler) http.Handler {
	reqs := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sayso_http_requests_total", Help: "HTTP requests by route, method and status.",
	}, []string{"route", "method", "status"})
	dur := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "sayso_http_request_duration_seconds", Help: "HTTP request duration by route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route", "method"})
	reg.MustRegister(reqs, dur)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
			next.ServeHTTP(sw, r)
			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			reqs.WithLabelValues(route, r.Method, strconv.Itoa(sw.code)).Inc()
			dur.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
