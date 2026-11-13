package review

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/auth"
	"github.com/alternayte/sayso/internal/findings"
	"github.com/alternayte/sayso/internal/publish"
	"github.com/alternayte/sayso/internal/review/signals"
	"github.com/alternayte/sayso/internal/runner"
)

// API is the reviews and rounds API of the UI.
type API struct {
	Pool     *pgxpool.Pool
	Temporal client.Client
	// Namespace is the Temporal namespace, for the links of admins.
	Namespace string
}

type RoundSummary struct {
	ID         string     `json:"id"`
	HeadSHA    string     `json:"head_sha"`
	Trigger    string     `json:"trigger"`
	Status     string     `json:"status"`
	Verdict    string     `json:"verdict,omitempty"`
	Risk       string     `json:"risk,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

type ReviewItem struct {
	ID         string        `json:"id"`
	Repository string        `json:"repository"`
	Number     int           `json:"number"`
	Author     string        `json:"author"`
	State      string        `json:"state"`
	Mode       string        `json:"mode"`
	LastRound  *RoundSummary `json:"last_round"`
}

type FindingBody struct {
	ID        string            `json:"id"`
	Label     string            `json:"label"`
	Severity  string            `json:"severity"`
	Category  string            `json:"category"`
	Title     string            `json:"title"`
	Body      string            `json:"body"`
	Path      string            `json:"path"`
	StartLine int               `json:"start_line"`
	EndLine   int               `json:"end_line"`
	Status    string            `json:"status"`
	Blocking  bool              `json:"blocking"`
	Evidence  runner.Evidence   `json:"evidence"`
	Challenge *runner.Challenge `json:"challenge,omitempty"`
}

type SignalBody struct {
	Type    string    `json:"type"`
	Actor   string    `json:"actor"`
	Finding string    `json:"finding,omitempty"`
	At      time.Time `json:"at"`
}

type ReviewBody struct {
	ReviewItem
	Rounds   []RoundSummary `json:"rounds"`
	Findings []FindingBody  `json:"findings"`
	Signals  []SignalBody   `json:"signals"`
	// TemporalURL is the workflow in the Temporal UI; only admins get it.
	TemporalURL string `json:"temporal_url,omitempty"`
}

type RoundBody struct {
	RoundSummary
	Repository  string             `json:"repository"`
	Number      int                `json:"number"`
	Mode        string             `json:"mode"`
	Assessment  *runner.Assessment `json:"assessment,omitempty"`
	Preview     *publish.Preview   `json:"preview,omitempty" doc:"What Sayso posts, rendered as Markdown; shadow mode posts nothing"`
	Config      json.RawMessage    `json:"config,omitempty"`
	SkillRef    *runner.SkillRef   `json:"skill_ref,omitempty"`
	PromptRef   json.RawMessage    `json:"prompt_ref,omitempty"`
	AgentRunID  string             `json:"agent_run_id,omitempty"`
	Findings    []FindingBody      `json:"findings"`
	CanCancel   bool               `json:"can_cancel"`
	TemporalURL string             `json:"temporal_url,omitempty"`
}

func (a *API) temporalURL(ctx context.Context, workflowID string) string {
	if u := auth.UserFrom(ctx); u == nil || !u.Role.AtLeast(auth.Admin) {
		return ""
	}
	ns := a.Namespace
	if ns == "" {
		ns = "default"
	}
	return "/temporal/namespaces/" + url.PathEscape(ns) + "/workflows/" + url.PathEscape(workflowID)
}

const roundCols = `ro.id::text, ro.head_sha, ro.trigger, ro.status, coalesce(ro.verdict, ''), coalesce(ro.risk, ''), ro.failure_reason, ro.created_at, ro.finished_at`

func scanRound(row pgx.Row, extra ...any) (RoundSummary, error) {
	var r RoundSummary
	err := row.Scan(append([]any{&r.ID, &r.HeadSHA, &r.Trigger, &r.Status, &r.Verdict, &r.Risk, &r.Reason, &r.CreatedAt, &r.FinishedAt}, extra...)...)
	return r, err
}

func findingBodies(fs []findings.Finding, keep func(findings.Finding) bool) []FindingBody {
	out := []FindingBody{}
	for _, f := range fs {
		if keep != nil && !keep(f) {
			continue
		}
		out = append(out, FindingBody{ID: f.ID.String(), Label: f.Label(), Severity: f.Severity, Category: f.Category, Title: f.Title, Body: f.Body, Path: f.Path,
			StartLine: f.StartLine, EndLine: f.EndLine, Status: f.Status, Blocking: f.Blocking, Evidence: f.Evidence, Challenge: f.Challenge})
	}
	return out
}

// Register adds the review and round operations.
func (a *API) Register(api huma.API, view, edit map[string]any, actor func(context.Context) audit.Actor) {
	tags := []string{"reviews"}
	huma.Register(api, huma.Operation{OperationID: "list-reviews", Method: http.MethodGet, Path: "/reviews", Tags: tags, Metadata: view},
		func(ctx context.Context, in *struct {
			Repository string `query:"repository" doc:"owner/name"`
			State      string `query:"state" enum:",open,paused,closed,merged"`
		}) (*struct{ Body []ReviewItem }, error) {
			rows, err := a.Pool.Query(ctx, `SELECT rv.id::text, rp.full_name, rv.pr_number, rv.author_login, rv.state, rp.mode,
				lr.id::text, lr.head_sha, lr.trigger, lr.status, coalesce(lr.verdict, ''), coalesce(lr.risk, ''), lr.failure_reason, lr.created_at, lr.finished_at
				FROM reviews rv JOIN repositories rp ON rp.id = rv.repository_id
				LEFT JOIN LATERAL (SELECT * FROM rounds WHERE review_id = rv.id ORDER BY created_at DESC LIMIT 1) lr ON true
				WHERE ($1 = '' OR lower(rp.full_name) = lower($1)) AND ($2 = '' OR rv.state = $2)
				ORDER BY coalesce(lr.created_at, rv.created_at) DESC LIMIT 200`, in.Repository, in.State)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			out := []ReviewItem{}
			for rows.Next() {
				var it ReviewItem
				var id, head, trig, status, verdict, risk, reason *string
				var created *time.Time
				var finished *time.Time
				if err := rows.Scan(&it.ID, &it.Repository, &it.Number, &it.Author, &it.State, &it.Mode, &id, &head, &trig, &status, &verdict, &risk, &reason, &created, &finished); err != nil {
					return nil, err
				}
				if id != nil {
					it.LastRound = &RoundSummary{ID: *id, HeadSHA: *head, Trigger: *trig, Status: *status, Verdict: *verdict, Risk: *risk, Reason: *reason, CreatedAt: *created, FinishedAt: finished}
				}
				out = append(out, it)
			}
			return &struct{ Body []ReviewItem }{out}, rows.Err()
		})

	huma.Register(api, huma.Operation{OperationID: "get-review", Method: http.MethodGet, Path: "/reviews/{owner}/{name}/{number}", Tags: tags, Metadata: view},
		func(ctx context.Context, in *struct {
			Owner  string `path:"owner"`
			Name   string `path:"name"`
			Number int    `path:"number"`
		}) (*struct{ Body ReviewBody }, error) {
			var b ReviewBody
			var reviewID uuid.UUID
			var wf string
			err := a.Pool.QueryRow(ctx, `SELECT rv.id, rp.full_name, rv.pr_number, rv.author_login, rv.state, rp.mode, rv.workflow_id FROM reviews rv
				JOIN repositories rp ON rp.id = rv.repository_id WHERE lower(rp.full_name) = lower($1) AND rv.pr_number = $2`, in.Owner+"/"+in.Name, in.Number).
				Scan(&reviewID, &b.Repository, &b.Number, &b.Author, &b.State, &b.Mode, &wf)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("review not found")
			}
			if err != nil {
				return nil, err
			}
			b.ID = reviewID.String()
			rows, err := a.Pool.Query(ctx, `SELECT `+roundCols+` FROM rounds ro WHERE ro.review_id = $1 ORDER BY ro.created_at DESC`, reviewID)
			if err != nil {
				return nil, err
			}
			b.Rounds = []RoundSummary{}
			for rows.Next() {
				r, err := scanRound(rows)
				if err != nil {
					rows.Close()
					return nil, err
				}
				b.Rounds = append(b.Rounds, r)
			}
			rows.Close()
			if len(b.Rounds) > 0 {
				b.LastRound = &b.Rounds[0]
			}
			tx, err := a.Pool.Begin(ctx)
			if err != nil {
				return nil, err
			}
			fs, err := findings.List(ctx, tx, reviewID)
			_ = tx.Rollback(ctx)
			if err != nil {
				return nil, err
			}
			b.Findings = findingBodies(fs, nil)
			sig, err := a.Pool.Query(ctx, `SELECT s.type, s.actor_login, coalesce('S-' || f.seq, ''), s.at FROM steering_signals s LEFT JOIN findings f ON f.id = s.finding_id
				WHERE s.review_id = $1 ORDER BY s.at DESC`, reviewID)
			if err != nil {
				return nil, err
			}
			b.Signals = []SignalBody{}
			for sig.Next() {
				var s SignalBody
				if err := sig.Scan(&s.Type, &s.Actor, &s.Finding, &s.At); err != nil {
					sig.Close()
					return nil, err
				}
				b.Signals = append(b.Signals, s)
			}
			sig.Close()
			b.TemporalURL = a.temporalURL(ctx, wf)
			return &struct{ Body ReviewBody }{b}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "get-round", Method: http.MethodGet, Path: "/rounds/{id}", Tags: tags, Metadata: view},
		func(ctx context.Context, in *struct {
			ID string `path:"id" format:"uuid"`
		}) (*struct{ Body RoundBody }, error) {
			id, err := uuid.Parse(in.ID)
			if err != nil {
				return nil, huma.Error404NotFound("round not found")
			}
			var b RoundBody
			var reviewID uuid.UUID
			var assessment, preview, config, skill, prompt []byte
			var run *uuid.UUID
			row := a.Pool.QueryRow(ctx, `SELECT `+roundCols+`, ro.review_id, rp.full_name, rv.pr_number, rp.mode, ro.assessment, ro.preview, ro.config_snapshot - 'spec', ro.skill_ref, ro.prompt_ref, ro.agent_run_id
				FROM rounds ro JOIN reviews rv ON rv.id = ro.review_id JOIN repositories rp ON rp.id = rv.repository_id WHERE ro.id = $1`, id)
			b.RoundSummary, err = scanRound(row, &reviewID, &b.Repository, &b.Number, &b.Mode, &assessment, &preview, &config, &skill, &prompt, &run)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("round not found")
			}
			if err != nil {
				return nil, err
			}
			if len(assessment) > 0 {
				b.Assessment = &runner.Assessment{}
				_ = json.Unmarshal(assessment, b.Assessment)
			}
			if len(preview) > 0 {
				b.Preview = &publish.Preview{}
				_ = json.Unmarshal(preview, b.Preview)
			}
			if len(skill) > 0 {
				b.SkillRef = &runner.SkillRef{}
				_ = json.Unmarshal(skill, b.SkillRef)
			}
			b.Config, b.PromptRef = config, prompt
			if run != nil {
				b.AgentRunID = run.String()
			}
			tx, err := a.Pool.Begin(ctx)
			if err != nil {
				return nil, err
			}
			fs, err := findings.List(ctx, tx, reviewID)
			_ = tx.Rollback(ctx)
			if err != nil {
				return nil, err
			}
			b.Findings = findingBodies(fs, func(f findings.Finding) bool { return f.FirstRoundID == id || f.LastRoundID == id })
			u := auth.UserFrom(ctx)
			b.CanCancel = u != nil && u.Role.AtLeast(auth.Maintainer) && (b.Status == "queued" || b.Status == "running")
			b.TemporalURL = a.temporalURL(ctx, "round/"+b.ID)
			return &struct{ Body RoundBody }{b}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "cancel-round", Method: http.MethodPost, Path: "/rounds/{id}/cancel", Tags: tags, Metadata: edit, DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, in *struct {
			ID string `path:"id" format:"uuid"`
		}) (*struct{}, error) {
			id, err := uuid.Parse(in.ID)
			if err != nil {
				return nil, huma.Error404NotFound("round not found")
			}
			var status, wf, full string
			err = a.Pool.QueryRow(ctx, `SELECT ro.status, rv.workflow_id, rp.full_name FROM rounds ro JOIN reviews rv ON rv.id = ro.review_id
				JOIN repositories rp ON rp.id = rv.repository_id WHERE ro.id = $1`, id).Scan(&status, &wf, &full)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("round not found")
			}
			if err != nil {
				return nil, err
			}
			if status != "queued" && status != "running" {
				return nil, huma.Error409Conflict("the round is " + status + "; only a queued or running round can be cancelled")
			}
			who := auth.UserFrom(ctx)
			if err := a.Temporal.SignalWorkflow(ctx, wf, "", signals.SignalCancelRound, signals.Signal{RoundID: id.String(), Actor: who.Login}); err != nil {
				return nil, err
			}
			return nil, pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
				return audit.Write(ctx, tx, actor(ctx), "round.cancel", full+" "+id.String(), nil)
			})
		})
}
