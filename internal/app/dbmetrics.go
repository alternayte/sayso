package app

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// dbMetrics exports the state that lives in PostgreSQL on each scrape
// (§18.3): rounds by status, agent run durations, model usage and cost,
// proxy errors, webhook deliveries and steering signals.
type dbMetrics struct {
	pool                                                              *pgxpool.Pool
	rounds, runDuration, tokens, cost, proxyErrors, webhooks, signals *prometheus.Desc
}

func newDBMetrics(pool *pgxpool.Pool) *dbMetrics {
	return &dbMetrics{
		pool:        pool,
		rounds:      prometheus.NewDesc("sayso_rounds", "Rounds by status.", []string{"status"}, nil),
		runDuration: prometheus.NewDesc("sayso_agent_run_duration_seconds", "Durations of finished agent runs.", []string{"kind"}, nil),
		tokens:      prometheus.NewDesc("sayso_model_tokens_total", "Model tokens through the model proxy.", []string{"direction"}, nil),
		cost:        prometheus.NewDesc("sayso_model_cost_usd_total", "Model cost through the model proxy in USD.", nil, nil),
		proxyErrors: prometheus.NewDesc("sayso_proxy_errors_total", "Model proxy calls that ended with a client or server error.", []string{"class"}, nil),
		webhooks:    prometheus.NewDesc("sayso_webhook_deliveries_total", "Webhook deliveries by result.", []string{"result"}, nil),
		signals:     prometheus.NewDesc("sayso_steering_signals_total", "Steering signals by type.", []string{"type"}, nil),
	}
}

func (m *dbMetrics) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{m.rounds, m.runDuration, m.tokens, m.cost, m.proxyErrors, m.webhooks, m.signals} {
		ch <- d
	}
}

func (m *dbMetrics) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	counts := func(desc *prometheus.Desc, kind prometheus.ValueType, sql string) {
		rows, err := m.pool.Query(ctx, sql)
		if err != nil {
			ch <- prometheus.NewInvalidMetric(desc, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var labels []string
			var v float64
			vals := rows.RawValues()
			n := len(vals) - 1
			dest := make([]any, n+1)
			labels = make([]string, n)
			for i := range labels {
				dest[i] = &labels[i]
			}
			dest[n] = &v
			if err := rows.Scan(dest...); err != nil {
				ch <- prometheus.NewInvalidMetric(desc, err)
				return
			}
			ch <- prometheus.MustNewConstMetric(desc, kind, v, labels...)
		}
	}
	// Each family lists every label value, so an empty table still shows zeros.
	counts(m.rounds, prometheus.GaugeValue, `SELECT s, count(r.id)::float8 FROM unnest(ARRAY['queued','running','publishing','done','failed','cancelled','budget_exceeded']) s
		LEFT JOIN rounds r ON r.status = s GROUP BY s`)
	counts(m.tokens, prometheus.CounterValue, `SELECT 'in', coalesce(sum(tokens_in), 0)::float8 FROM model_usage UNION ALL SELECT 'out', coalesce(sum(tokens_out), 0)::float8 FROM model_usage`)
	counts(m.cost, prometheus.CounterValue, `SELECT coalesce(sum(cost_usd), 0)::float8 FROM model_usage`)
	counts(m.proxyErrors, prometheus.CounterValue, `SELECT c, count(u.id)::float8 FROM unnest(ARRAY['4xx','5xx']) c
		LEFT JOIN model_usage u ON (c = '4xx' AND u.status BETWEEN 400 AND 499) OR (c = '5xx' AND u.status >= 500) GROUP BY c`)
	counts(m.webhooks, prometheus.CounterValue, `SELECT r, count(w.delivery_id)::float8 FROM unnest(ARRAY['ok','failed','pending']) r
		LEFT JOIN webhook_deliveries w ON r = CASE WHEN w.error <> '' THEN 'failed' WHEN w.handled_at IS NULL THEN 'pending' ELSE 'ok' END GROUP BY r`)
	counts(m.signals, prometheus.CounterValue, `SELECT t, count(s.id)::float8 FROM unnest(ARRAY['dismissed','overridden','conceded','resolved_unfixed','human_comment_unflagged','rerun_requested']) t
		LEFT JOIN steering_signals s ON s.type = t GROUP BY t`)
	rows, err := m.pool.Query(ctx, `SELECT k, count(a.id), coalesce(sum(extract(epoch FROM a.finished_at - a.started_at)), 0)::float8
		FROM unnest(ARRAY['round','reply','fix','noop']) k
		LEFT JOIN agent_runs a ON a.kind = k AND a.finished_at IS NOT NULL AND a.started_at IS NOT NULL GROUP BY k`)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(m.runDuration, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n uint64
		var sum float64
		if err := rows.Scan(&kind, &n, &sum); err != nil {
			return
		}
		ch <- prometheus.MustNewConstSummary(m.runDuration, n, sum, nil, kind)
	}
}
