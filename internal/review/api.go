package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	"github.com/alternayte/sayso/internal/settings"
)

// API is the reviews and rounds API of the UI.
type API struct {
	Pool     *pgxpool.Pool
	Temporal client.Client
	// Namespace is the Temporal namespace, for the links of admins.
	Namespace string
	// Activities dismisses findings from the UI as the command does.
	Activities *Activities
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
	Title      string        `json:"title"`
	HTMLURL    string        `json:"html_url" doc:"The pull request on GitHub"`
	HeadRef    string        `json:"head_ref"`
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
	// Suggestion replaces start_line to end_line at the head commit.
	Suggestion  string `json:"suggestion,omitempty"`
	PreExisting bool   `json:"pre_existing"`
}

type SignalBody struct {
	Type    string    `json:"type"`
	Actor   string    `json:"actor"`
	Finding string    `json:"finding,omitempty"`
	At      time.Time `json:"at"`
}

type ReviewBody struct {
	ReviewItem
	// SummaryURL and CheckURL exist when Sayso posted them.
	SummaryURL string         `json:"summary_url,omitempty"`
	CheckURL   string         `json:"check_url,omitempty"`
	Rounds     []RoundSummary `json:"rounds"`
	Findings   []FindingBody  `json:"findings"`
	Signals    []SignalBody   `json:"signals"`
	// Preview is what the latest round posts, or would post in shadow mode.
	Preview        *publish.Preview    `json:"preview,omitempty"`
	FailedCommands []FailedCommandBody `json:"failed_commands"`
	Fixes          []FixBody           `json:"fixes"`
	// TemporalURL is the workflow in the Temporal UI; only admins get it.
	TemporalURL string `json:"temporal_url,omitempty"`
}

// FailedCommandBody is a command that failed after its retries.
type FailedCommandBody struct {
	ID        string     `json:"id"`
	Author    string     `json:"author"`
	Command   string     `json:"command"`
	Reason    string     `json:"reason"`
	At        time.Time  `json:"at"`
	RetriedAt *time.Time `json:"retried_at"`
}

// FixBody is one fix run and its fix pull request.
type FixBody struct {
	ID       string    `json:"id"`
	Findings []string  `json:"findings" doc:"Labels, for example S-2"`
	Status   string    `json:"status"`
	Reason   string    `json:"reason,omitempty"`
	PullURL  string    `json:"pull_url,omitempty"`
	Actor    string    `json:"actor"`
	At       time.Time `json:"at"`
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
	Signals     []SignalBody       `json:"signals"`
	Steering    *SteeringNote      `json:"steering_review,omitempty"`
}

// SteeringNote is the saved steering review of a round.
type SteeringNote struct {
	Tags   []string  `json:"tags"`
	Note   string    `json:"note"`
	Author string    `json:"author"`
	At     time.Time `json:"at"`
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

func (a *API) webURL(ctx context.Context) string {
	u := "https://github.com"
	if a.Activities != nil && a.Activities.Settings != nil {
		_ = a.Activities.Settings.Decode(ctx, settings.GitHubWebURL, &u)
	}
	return strings.TrimRight(u, "/")
}

// roundStatuses maps a round status filter to the stored statuses.
var roundStatuses = map[string][]string{
	"queued": {"queued"}, "running": {"running", "publishing"}, "done": {"done"},
	"failed": {"failed", "budget_exceeded"}, "cancelled": {"cancelled"},
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
			StartLine: f.StartLine, EndLine: f.EndLine, Status: f.Status, Blocking: f.Blocking, Evidence: f.Evidence, Challenge: f.Challenge,
			Suggestion: f.Suggestion, PreExisting: f.PreExisting})
	}
	return out
}

// Register adds the review and round operations.
func (a *API) Register(api huma.API, view, edit map[string]any, actor func(context.Context) audit.Actor) {
	tags := []string{"reviews"}
	huma.Register(api, huma.Operation{OperationID: "get-effective-config", Method: http.MethodGet, Path: "/repositories/{owner}/{name}/effective", Tags: []string{"repositories"}, Metadata: view},
		func(ctx context.Context, in *struct {
			Owner string `path:"owner"`
			Name  string `path:"name"`
		}) (*struct{ Body EffectiveBody }, error) {
			b, err := a.Activities.EffectiveConfig(ctx, in.Owner+"/"+in.Name)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("repository not found")
			}
			return &struct{ Body EffectiveBody }{b}, err
		})
	huma.Register(api, huma.Operation{OperationID: "list-reviews", Method: http.MethodGet, Path: "/reviews", Tags: tags, Metadata: view},
		func(ctx context.Context, in *struct {
			Repository  string `query:"repository" doc:"owner/name"`
			State       string `query:"state" enum:",open,paused,closed,merged"`
			RoundStatus string `query:"round_status" enum:",queued,running,done,failed,cancelled" doc:"Status of the latest round"`
		}) (*struct{ Body []ReviewItem }, error) {
			statuses := roundStatuses[in.RoundStatus]
			if statuses == nil {
				statuses = []string{}
			}
			rows, err := a.Pool.Query(ctx, `SELECT rv.id::text, rp.full_name, rv.pr_number, rv.title, rv.head_ref, rv.author_login, rv.state, rp.mode,
				lr.id::text, lr.head_sha, lr.trigger, lr.status, coalesce(lr.verdict, ''), coalesce(lr.risk, ''), lr.failure_reason, lr.created_at, lr.finished_at
				FROM reviews rv JOIN repositories rp ON rp.id = rv.repository_id
				LEFT JOIN LATERAL (SELECT * FROM rounds WHERE review_id = rv.id ORDER BY created_at DESC LIMIT 1) lr ON true
				WHERE ($1 = '' OR lower(rp.full_name) = lower($1)) AND ($2 = '' OR rv.state = $2) AND (cardinality($3::text[]) = 0 OR lr.status = ANY($3))
				ORDER BY coalesce(lr.created_at, rv.created_at) DESC LIMIT 200`, in.Repository, in.State, statuses)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			web := a.webURL(ctx)
			out := []ReviewItem{}
			for rows.Next() {
				var it ReviewItem
				var id, head, trig, status, verdict, risk, reason *string
				var created *time.Time
				var finished *time.Time
				if err := rows.Scan(&it.ID, &it.Repository, &it.Number, &it.Title, &it.HeadRef, &it.Author, &it.State, &it.Mode, &id, &head, &trig, &status, &verdict, &risk, &reason, &created, &finished); err != nil {
					return nil, err
				}
				it.HTMLURL = fmt.Sprintf("%s/%s/pull/%d", web, it.Repository, it.Number)
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
			var summaryID *int64
			err := a.Pool.QueryRow(ctx, `SELECT rv.id, rp.full_name, rv.pr_number, rv.title, rv.head_ref, rv.author_login, rv.state, rp.mode, rv.workflow_id, rv.summary_comment_id
				FROM reviews rv JOIN repositories rp ON rp.id = rv.repository_id WHERE lower(rp.full_name) = lower($1) AND rv.pr_number = $2`, in.Owner+"/"+in.Name, in.Number).
				Scan(&reviewID, &b.Repository, &b.Number, &b.Title, &b.HeadRef, &b.Author, &b.State, &b.Mode, &wf, &summaryID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("review not found")
			}
			if err != nil {
				return nil, err
			}
			b.ID = reviewID.String()
			web := a.webURL(ctx)
			b.HTMLURL = fmt.Sprintf("%s/%s/pull/%d", web, b.Repository, b.Number)
			if summaryID != nil {
				b.SummaryURL = fmt.Sprintf("%s#issuecomment-%d", b.HTMLURL, *summaryID)
			}
			var checkID *int64
			var preview []byte
			if err := a.Pool.QueryRow(ctx, `SELECT check_run_id, preview FROM rounds WHERE review_id = $1 ORDER BY created_at DESC LIMIT 1`, reviewID).Scan(&checkID, &preview); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			if checkID != nil {
				b.CheckURL = fmt.Sprintf("%s/%s/runs/%d", web, b.Repository, *checkID)
			}
			if len(preview) > 0 {
				b.Preview = &publish.Preview{}
				_ = json.Unmarshal(preview, b.Preview)
			}
			if b.FailedCommands, err = a.failedCommands(ctx, reviewID); err != nil {
				return nil, err
			}
			if b.Fixes, err = a.fixes(ctx, reviewID, b.HTMLURL); err != nil {
				return nil, err
			}
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
			sig, err := a.Pool.Query(ctx, `SELECT s.type, s.actor_login, coalesce('S-' || f.seq, ''), s.at FROM steering_signals s LEFT JOIN findings f ON f.id = s.finding_id
				WHERE s.round_id = $1 ORDER BY s.at`, id)
			if err != nil {
				return nil, err
			}
			b.Signals = []SignalBody{}
			for sig.Next() {
				var x SignalBody
				if err := sig.Scan(&x.Type, &x.Actor, &x.Finding, &x.At); err != nil {
					sig.Close()
					return nil, err
				}
				b.Signals = append(b.Signals, x)
			}
			sig.Close()
			var note SteeringNote
			if err := a.Pool.QueryRow(ctx, `SELECT tags, note, author_login, created_at FROM steering_reviews WHERE round_id = $1`, id).Scan(&note.Tags, &note.Note, &note.Author, &note.At); err == nil {
				b.Steering = &note
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
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

	huma.Register(api, huma.Operation{OperationID: "dismiss-finding", Method: http.MethodPost, Path: "/findings/{id}/dismiss", Tags: tags, Metadata: edit},
		func(ctx context.Context, in *struct {
			ID   string `path:"id" format:"uuid"`
			Body struct {
				Reason string `json:"reason" minLength:"1" maxLength:"500"`
			}
		}) (*struct{ Body FindingBody }, error) {
			id, err := uuid.Parse(in.ID)
			if err != nil {
				return nil, huma.Error404NotFound("finding not found")
			}
			var reviewID uuid.UUID
			var status string
			err = a.Pool.QueryRow(ctx, `SELECT review_id, status FROM findings WHERE id = $1`, id).Scan(&reviewID, &status)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("finding not found")
			}
			if err != nil {
				return nil, err
			}
			if status != findings.StatusOpen {
				return nil, huma.Error409Conflict("the finding is " + status + "; only an open finding can be dismissed")
			}
			p, err := a.Activities.pr(ctx, reviewID)
			if err != nil {
				return nil, err
			}
			f, err := a.Activities.findingByID(ctx, reviewID, id)
			if err != nil {
				return nil, err
			}
			who := auth.UserFrom(ctx)
			if err := a.Activities.dismiss(ctx, p, f, who.Login, in.Body.Reason); err != nil {
				return nil, err
			}
			if err := pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
				return audit.Write(ctx, tx, actor(ctx), "finding.dismiss", p.full+" "+f.Label(), map[string]any{"reason": in.Body.Reason})
			}); err != nil {
				return nil, err
			}
			f.Status = findings.StatusDismissed
			return &struct{ Body FindingBody }{findingBodies([]findings.Finding{*f}, nil)[0]}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "retry-failed-command", Method: http.MethodPost, Path: "/failed-commands/{id}/retry", Tags: tags, Metadata: edit, DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, in *struct {
			ID string `path:"id" format:"uuid"`
		}) (*struct{}, error) {
			id, err := uuid.Parse(in.ID)
			if err != nil {
				return nil, huma.Error404NotFound("failed command not found")
			}
			var s signals.Signal
			var wf, full string
			err = a.Pool.QueryRow(ctx, `SELECT fc.comment_id, fc.source, fc.in_reply_to, rv.workflow_id, rp.full_name FROM failed_commands fc
				JOIN reviews rv ON rv.id = fc.review_id JOIN repositories rp ON rp.id = rv.repository_id WHERE fc.id = $1`, id).
				Scan(&s.CommentID, &s.CommentSource, &s.InReplyTo, &wf, &full)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("failed command not found")
			}
			if err != nil {
				return nil, err
			}
			if err := a.Temporal.SignalWorkflow(ctx, wf, "", signals.SignalCommand, s); err != nil {
				return nil, err
			}
			return nil, pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE failed_commands SET retried_at = now() WHERE id = $1`, id); err != nil {
					return err
				}
				return audit.Write(ctx, tx, actor(ctx), "command.retry", full+" "+id.String(), nil)
			})
		})

	huma.Register(api, huma.Operation{OperationID: "fix-findings", Method: http.MethodPost, Path: "/reviews/{owner}/{name}/{number}/fix", Tags: tags, Metadata: edit, DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, in *struct {
			Owner  string `path:"owner"`
			Name   string `path:"name"`
			Number int    `path:"number"`
			Body   struct {
				Findings []string `json:"findings" minItems:"1" doc:"Labels, for example S-2"`
			}
		}) (*struct{ Body FixBody }, error) {
			var reviewID uuid.UUID
			var wf string
			err := a.Pool.QueryRow(ctx, `SELECT rv.id, rv.workflow_id FROM reviews rv JOIN repositories rp ON rp.id = rv.repository_id
				WHERE lower(rp.full_name) = lower($1) AND rv.pr_number = $2`, in.Owner+"/"+in.Name, in.Number).Scan(&reviewID, &wf)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("review not found")
			}
			if err != nil {
				return nil, err
			}
			var seqs []int
			for _, l := range in.Body.Findings {
				m := findingArg.FindStringSubmatch(strings.ToUpper(l))
				if m == nil {
					return nil, huma.Error422UnprocessableEntity("findings: " + l + " is not a finding ID such as S-2")
				}
				n, _ := strconv.Atoi(m[1])
				seqs = append(seqs, n)
			}
			who := auth.UserFrom(ctx)
			p, err := a.Activities.pr(ctx, reviewID)
			if err != nil {
				return nil, err
			}
			fs, why, err := a.Activities.fixTargets(ctx, p, Command{Name: "fix", Findings: seqs})
			if err != nil {
				return nil, err
			}
			if why != "" {
				return nil, huma.Error409Conflict(why)
			}
			workID, err := a.Activities.queueFix(ctx, p, fs, comment{source: "issue", author: who.Login})
			if err != nil {
				return nil, err
			}
			if err := a.Temporal.SignalWorkflow(ctx, wf, "", signals.SignalWork, signals.Signal{WorkID: workID.String(), Actor: who.Login}); err != nil {
				return nil, err
			}
			if err := pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
				return audit.Write(ctx, tx, actor(ctx), "finding.fix", p.full+" "+strings.Join(in.Body.Findings, " "), nil)
			}); err != nil {
				return nil, err
			}
			labels := make([]string, 0, len(fs))
			for _, f := range fs {
				labels = append(labels, f.Label())
			}
			return &struct{ Body FixBody }{FixBody{ID: workID.String(), Findings: labels, Status: "queued", Actor: who.Login, At: time.Now()}}, nil
		})
}

func (a *API) failedCommands(ctx context.Context, reviewID uuid.UUID) ([]FailedCommandBody, error) {
	rows, err := a.Pool.Query(ctx, `SELECT id::text, actor_login, command, reason, created_at, retried_at FROM failed_commands WHERE review_id = $1 ORDER BY created_at DESC`, reviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FailedCommandBody{}
	for rows.Next() {
		var c FailedCommandBody
		if err := rows.Scan(&c.ID, &c.Author, &c.Command, &c.Reason, &c.At, &c.RetriedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (a *API) fixes(ctx context.Context, reviewID uuid.UUID, pullURL string) ([]FixBody, error) {
	rows, err := a.Pool.Query(ctx, `SELECT w.id::text, w.status, w.reason, w.actor_login, w.created_at, coalesce((w.outcome->>'pull')::int, 0),
			ARRAY(SELECT 'S-' || f.seq FROM findings f WHERE f.id = ANY(CASE WHEN cardinality(w.finding_ids) > 0 THEN w.finding_ids ELSE ARRAY[w.finding_id] END) ORDER BY f.seq)
		FROM agent_work w WHERE w.review_id = $1 AND w.kind = 'fix' ORDER BY w.created_at DESC`, reviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	base := pullURL[:strings.LastIndex(pullURL, "/")+1]
	out := []FixBody{}
	for rows.Next() {
		var f FixBody
		var pull int
		if err := rows.Scan(&f.ID, &f.Status, &f.Reason, &f.Actor, &f.At, &pull, &f.Findings); err != nil {
			return nil, err
		}
		if pull > 0 {
			f.PullURL = base + strconv.Itoa(pull)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
