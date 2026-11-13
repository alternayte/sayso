package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/secrets"
	"github.com/alternayte/sayso/internal/settings"
	"github.com/alternayte/sayso/internal/storage"
)

// ReportAttempts is how often a report is tried before it shows on the
// Diagnostics page (§14.2).
const ReportAttempts = 5

// Reporter sends run events and outcomes to Skael with the reporter key.
type Reporter struct {
	Pool     *pgxpool.Pool
	Secrets  *secrets.Store
	Settings *settings.Store
	Stores   *storage.Registry
	HTTP     *http.Client
	// BaseURL is the public URL of the UI, for the evidence link.
	BaseURL string
	// Backoff is the first wait between attempts; it doubles.
	Backoff time.Duration
}

type runEvent struct {
	Skill         string    `json:"skill"`
	Version       int       `json:"version"`
	Agent         string    `json:"agent"`
	Role          string    `json:"role"`
	ExternalRunID string    `json:"external_run_id"`
	Time          time.Time `json:"time"`
	Set           string    `json:"set,omitempty"`
	ResolutionID  string    `json:"resolution_id,omitempty"`
}

type outcome struct {
	ExternalRunID string `json:"external_run_id"`
	Outcome       string `json:"outcome"`
	Repairs       int    `json:"repairs"`
	ReviewVerdict string `json:"review_verdict,omitempty"`
	EvidenceURL   string `json:"evidence_url,omitempty"`
	Set           string `json:"set,omitempty"`
	ResolutionID  string `json:"resolution_id,omitempty"`
}

var outcomes = map[string]string{"done": "succeeded", "failed": "failed", "budget_exceeded": "failed", "cancelled": "cancelled"}

// ReportRound reports the skills that the round's run loaded and the round
// outcome. A second report replaces the first in Skael. Rounds without a
// Skael set and rounds that did not end send nothing.
func (r *Reporter) ReportRound(ctx context.Context, roundID uuid.UUID) error {
	var url string
	if err := r.Settings.Decode(ctx, settings.SkaelURL, &url); err != nil || url == "" {
		return err
	}
	var status, reviewID, head string
	var verdict *string
	var override []byte
	err := r.Pool.QueryRow(ctx, `SELECT status, verdict, override, review_id::text, head_sha FROM rounds WHERE id = $1`, roundID).Scan(&status, &verdict, &override, &reviewID, &head)
	if err != nil {
		return err
	}
	result, finished, err := r.result(ctx, roundID)
	if err != nil || result == nil || result.SkaelManifest == nil {
		return err
	}
	kind, ended := outcomes[status]
	if !ended {
		return nil
	}
	var repairs int
	if err := r.Pool.QueryRow(ctx, `SELECT count(*) FROM steering_signals WHERE round_id = $1`, roundID).Scan(&repairs); err != nil {
		return err
	}
	human := "none"
	var later bool
	if err := r.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rounds WHERE review_id = $1 AND head_sha <> $2 AND created_at > (SELECT created_at FROM rounds WHERE id = $3))`,
		reviewID, head, roundID).Scan(&later); err != nil {
		return err
	}
	switch {
	case len(override) > 0 && string(override) != "null":
		human = "overridden"
	case later:
		human = "changes_made"
	}
	v := "none"
	if verdict != nil {
		v = *verdict
	}
	m := result.SkaelManifest
	var events []runEvent
	for _, s := range result.SkillsLoaded {
		if s.Reported {
			continue
		}
		role := "reviewer"
		if s.Stage == runner.StageChallenge {
			role = "challenger"
		}
		events = append(events, runEvent{Skill: s.Name, Version: s.Version, Agent: "pi", Role: role, ExternalRunID: roundID.String(), Time: finished, Set: m.Set, ResolutionID: m.ResolutionID})
	}
	out := outcome{ExternalRunID: roundID.String(), Outcome: kind, Repairs: repairs, ReviewVerdict: v + "; " + human,
		EvidenceURL: strings.TrimRight(r.BaseURL, "/") + "/rounds/" + roundID.String(), Set: m.Set, ResolutionID: m.ResolutionID}
	key, err := r.Secrets.Value(ctx, ReporterKeySecret)
	if err != nil {
		return err
	}
	attempts, err := r.retry(ctx, func() error {
		if len(events) > 0 {
			// Skael keeps every event; the key makes a repeat report store nothing.
			if err := r.post(ctx, url+"/api/events/batch", key, map[string]any{"events": events}, "sayso-"+roundID.String()+"-"+result.RunID); err != nil {
				return err
			}
		}
		return r.post(ctx, url+"/api/outcomes", key, out, "")
	})
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if _, dbErr := r.Pool.Exec(context.WithoutCancel(ctx), `INSERT INTO skael_reports (round_id, attempts, last_error, reported_at, updated_at)
		VALUES ($1, $2, $3, CASE WHEN $3 = '' THEN now() END, now())
		ON CONFLICT (round_id) DO UPDATE SET attempts = skael_reports.attempts + EXCLUDED.attempts, last_error = EXCLUDED.last_error,
		reported_at = COALESCE(EXCLUDED.reported_at, skael_reports.reported_at), updated_at = now()`, roundID, attempts, msg); dbErr != nil {
		return errors.Join(err, dbErr)
	}
	return err
}

// result reads result.json of the round's latest agent run.
func (r *Reporter) result(ctx context.Context, roundID uuid.UUID) (*runner.Result, time.Time, error) {
	var storeID uuid.UUID
	var key string
	var finished *time.Time
	// A run that ended without result.json (cancelled, failed) still has the
	// manifest that the pull uploaded, so the outcome reaches Skael.
	var name string
	err := r.Pool.QueryRow(ctx, `SELECT a.store_id, a.key, ar.finished_at, a.name FROM agent_runs ar JOIN artifacts a ON a.run_id = ar.id AND a.name IN ('result.json', 'manifest.json')
		WHERE ar.round_id = $1 ORDER BY ar.created_at DESC, a.name = 'result.json' DESC LIMIT 1`, roundID).Scan(&storeID, &key, &finished, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	st, err := r.Stores.ByID(ctx, storeID)
	if err != nil {
		return nil, time.Time{}, err
	}
	rc, err := st.Get(ctx, key)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer func() { _ = rc.Close() }()
	var res runner.Result
	if name == "manifest.json" {
		var m runner.SkaelManifest
		if err := json.NewDecoder(rc).Decode(&m); err != nil {
			return nil, time.Time{}, err
		}
		res.SkaelManifest = &m
	} else if err := json.NewDecoder(rc).Decode(&res); err != nil {
		return nil, time.Time{}, err
	}
	at := time.Now()
	if finished != nil {
		at = *finished
	}
	return &res, at.UTC(), nil
}

// retry runs f up to ReportAttempts times and returns the count of attempts.
func (r *Reporter) retry(ctx context.Context, f func() error) (int, error) {
	wait := r.Backoff
	if wait == 0 {
		wait = time.Second
	}
	var err error
	for i := 1; i <= ReportAttempts; i++ {
		if err = f(); err == nil {
			return i, nil
		}
		if i == ReportAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return i, ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
	return ReportAttempts, fmt.Errorf("skael report failed after %d attempts: %w", ReportAttempts, err)
}

func (r *Reporter) post(ctx context.Context, url, key string, body any, idempotency string) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	if idempotency != "" {
		req.Header.Set("Idempotency-Key", idempotency)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return fmt.Errorf("POST %s: status %d: %s", url, resp.StatusCode, msg)
	}
	return nil
}
