// Package steering holds the steering queue, steering reviews, the export
// case file and the cost views (§15.4, REQ-OBS-008 to -010).
package steering

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/auth"
	"github.com/alternayte/sayso/internal/runner"
)

// API is the steering and usage API.
type API struct {
	Pool *pgxpool.Pool
}

type QueueItem struct {
	RoundID     string    `json:"round_id"`
	Repository  string    `json:"repository"`
	Number      int       `json:"number"`
	HeadSHA     string    `json:"head_sha"`
	Verdict     string    `json:"verdict,omitempty"`
	SkillSource string    `json:"skill_source,omitempty"`
	SkillVer    string    `json:"skill_version,omitempty"`
	Profile     string    `json:"profile,omitempty"`
	Signals     []string  `json:"signals" doc:"Signal types of the round"`
	LastSignal  time.Time `json:"last_signal"`
}

type QueueFilter struct {
	Repository    string `query:"repository" doc:"owner/name"`
	SkillSource   string `query:"skill_source" enum:",repository,skael,builtin"`
	SkillVersion  string `query:"skill_version"`
	PromptVersion string `query:"prompt_version" doc:"A prompt layer version or hash; matches any layer"`
	Profile       string `query:"profile" doc:"Profile of the review stage"`
	SignalType    string `query:"signal_type" enum:",dismissed,overridden,conceded,resolved_unfixed,human_comment_unflagged,rerun_requested"`
	From          string `query:"from" format:"date"`
	To            string `query:"to" format:"date"`
}

// Queue lists the rounds with signals and no steering review (§15.4).
func (a *API) Queue(ctx context.Context, f QueueFilter) ([]QueueItem, error) {
	rows, err := a.Pool.Query(ctx, `
		SELECT ro.id::text, rp.full_name, rv.pr_number, ro.head_sha, coalesce(ro.verdict, ''), coalesce(ro.skill_ref->>'source', ''),
			coalesce(ro.skill_ref->>'version', ''), coalesce(ro.config_snapshot->'profiles'->>'review', ''),
			array_agg(DISTINCT s.type ORDER BY s.type), max(s.at)
		FROM steering_signals s
		JOIN rounds ro ON ro.id = s.round_id JOIN reviews rv ON rv.id = ro.review_id JOIN repositories rp ON rp.id = rv.repository_id
		WHERE NOT EXISTS (SELECT 1 FROM steering_reviews sr WHERE sr.round_id = ro.id)
			AND ($1 = '' OR lower(rp.full_name) = lower($1))
			AND ($2 = '' OR ro.skill_ref->>'source' = $2)
			AND ($3 = '' OR ro.skill_ref->>'version' = $3)
			AND ($4 = '' OR $4 IN (SELECT value FROM jsonb_each_text(coalesce(ro.prompt_ref, '{}')) UNION SELECT value FROM jsonb_each_text(coalesce(ro.prompt_ref->'stages', '{}'))))
			AND ($5 = '' OR ro.config_snapshot->'profiles'->>'review' = $5)
			AND ($6 = '' OR EXISTS (SELECT 1 FROM steering_signals x WHERE x.round_id = ro.id AND x.type = $6))
			AND ($7 = '' OR s.at >= $7::date)
			AND ($8 = '' OR s.at < $8::date + 1)
		GROUP BY ro.id, rp.full_name, rv.pr_number
		ORDER BY max(s.at) DESC LIMIT 500`,
		f.Repository, f.SkillSource, f.SkillVersion, f.PromptVersion, f.Profile, f.SignalType, f.From, f.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueueItem{}
	for rows.Next() {
		var q QueueItem
		if err := rows.Scan(&q.RoundID, &q.Repository, &q.Number, &q.HeadSHA, &q.Verdict, &q.SkillSource, &q.SkillVer, &q.Profile, &q.Signals, &q.LastSignal); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

type ReviewInput struct {
	Tags []string `json:"tags" maxItems:"20"`
	Note string   `json:"note" maxLength:"4000"`
}

type SteeringReview struct {
	Tags      []string  `json:"tags"`
	Note      string    `json:"note"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

// CaseFile is the export case of §15.4.
type CaseFile struct {
	Version     int              `json:"version" doc:"Format version, 1"`
	ExportedAt  time.Time        `json:"exported_at"`
	PullRequest CasePull         `json:"pull_request"`
	Round       CaseRound        `json:"round"`
	Config      json.RawMessage  `json:"config_snapshot"`
	SkillRef    *runner.SkillRef `json:"skill_ref"`
	PromptRef   json.RawMessage  `json:"prompt_ref"`
	Findings    []CaseFinding    `json:"findings"`
	Signals     []CaseSignal     `json:"signals"`
	Review      *SteeringReview  `json:"steering_review"`
}

type CasePull struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	URL        string `json:"url"`
	Author     string `json:"author"`
}

type CaseRound struct {
	ID      string `json:"id"`
	HeadSHA string `json:"head_sha"`
	BaseSHA string `json:"base_sha"`
	Status  string `json:"status"`
	Verdict string `json:"verdict"`
	Risk    string `json:"risk"`
}

type CaseFinding struct {
	ID        string            `json:"id"`
	Severity  string            `json:"severity"`
	Category  string            `json:"category"`
	Title     string            `json:"title"`
	Path      string            `json:"path"`
	StartLine int               `json:"start_line"`
	EndLine   int               `json:"end_line"`
	Status    string            `json:"status"`
	Blocking  bool              `json:"blocking"`
	Evidence  runner.Evidence   `json:"evidence"`
	Challenge *runner.Challenge `json:"challenge,omitempty"`
}

type CaseSignal struct {
	Type    string          `json:"type"`
	Actor   string          `json:"actor"`
	Finding string          `json:"finding,omitempty"`
	Detail  json.RawMessage `json:"detail"`
	At      time.Time       `json:"at"`
}

// Case builds the export case of a round.
func (a *API) Case(ctx context.Context, roundID uuid.UUID, webURL string) (CaseFile, error) {
	c := CaseFile{Version: 1, ExportedAt: time.Now().UTC()}
	var skill, prompt, config []byte
	var reviewID uuid.UUID
	err := a.Pool.QueryRow(ctx, `SELECT rv.id, rp.full_name, rv.pr_number, rv.author_login, ro.id::text, ro.head_sha, ro.base_sha, ro.status, coalesce(ro.verdict, ''),
			coalesce(ro.risk, ''), coalesce(ro.config_snapshot - 'spec', 'null'), ro.skill_ref, coalesce(ro.prompt_ref, 'null')
		FROM rounds ro JOIN reviews rv ON rv.id = ro.review_id JOIN repositories rp ON rp.id = rv.repository_id WHERE ro.id = $1`, roundID).
		Scan(&reviewID, &c.PullRequest.Repository, &c.PullRequest.Number, &c.PullRequest.Author, &c.Round.ID, &c.Round.HeadSHA, &c.Round.BaseSHA, &c.Round.Status,
			&c.Round.Verdict, &c.Round.Risk, &config, &skill, &prompt)
	if err != nil {
		return c, err
	}
	c.PullRequest.URL = fmt.Sprintf("%s/%s/pull/%d", strings.TrimRight(webURL, "/"), c.PullRequest.Repository, c.PullRequest.Number)
	c.Config, c.PromptRef = config, prompt
	if len(skill) > 0 {
		c.SkillRef = &runner.SkillRef{}
		_ = json.Unmarshal(skill, c.SkillRef)
	}
	rows, err := a.Pool.Query(ctx, `SELECT 'S-' || seq, severity, category, title, path, start_line, end_line, status, blocking, evidence, challenge
		FROM findings WHERE review_id = $1 AND (first_round_id = $2 OR last_round_id = $2) ORDER BY seq`, reviewID, roundID)
	if err != nil {
		return c, err
	}
	c.Findings = []CaseFinding{}
	for rows.Next() {
		var f CaseFinding
		var ev, ch []byte
		if err := rows.Scan(&f.ID, &f.Severity, &f.Category, &f.Title, &f.Path, &f.StartLine, &f.EndLine, &f.Status, &f.Blocking, &ev, &ch); err != nil {
			rows.Close()
			return c, err
		}
		_ = json.Unmarshal(ev, &f.Evidence)
		if len(ch) > 0 && string(ch) != "null" {
			f.Challenge = &runner.Challenge{}
			_ = json.Unmarshal(ch, f.Challenge)
		}
		c.Findings = append(c.Findings, f)
	}
	rows.Close()
	sig, err := a.Pool.Query(ctx, `SELECT s.type, s.actor_login, coalesce('S-' || f.seq, ''), s.detail, s.at FROM steering_signals s LEFT JOIN findings f ON f.id = s.finding_id
		WHERE s.round_id = $1 ORDER BY s.at`, roundID)
	if err != nil {
		return c, err
	}
	c.Signals = []CaseSignal{}
	for sig.Next() {
		var s CaseSignal
		if err := sig.Scan(&s.Type, &s.Actor, &s.Finding, &s.Detail, &s.At); err != nil {
			sig.Close()
			return c, err
		}
		c.Signals = append(c.Signals, s)
	}
	sig.Close()
	var r SteeringReview
	err = a.Pool.QueryRow(ctx, `SELECT tags, note, author_login, created_at FROM steering_reviews WHERE round_id = $1`, roundID).Scan(&r.Tags, &r.Note, &r.Author, &r.CreatedAt)
	if err == nil {
		c.Review = &r
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	return c, nil
}

type UsageRow struct {
	Key       string  `json:"key" doc:"Round ID, owner/name, profile or YYYY-MM"`
	Label     string  `json:"label"`
	TokensIn  int64   `json:"tokens_in"`
	TokensOut int64   `json:"tokens_out"`
	CostUSD   float64 `json:"cost_usd"`
}

var usageKeys = map[string]string{
	"round":      `coalesce(ar.round_id::text, ar.id::text)`,
	"repository": `coalesce(rp.full_name, '')`,
	"profile":    `coalesce((SELECT m->>'profile' FROM jsonb_array_elements(ar.models) m WHERE m->>'provider' = u.provider AND m->>'model' = u.model LIMIT 1), '')`,
	"month":      `to_char(date_trunc('month', u.at), 'YYYY-MM')`,
}

// Usage sums model_usage per round, repository, profile or month (REQ-OBS-010).
func (a *API) Usage(ctx context.Context, group string) ([]UsageRow, error) {
	key, ok := usageKeys[group]
	if !ok {
		return nil, fmt.Errorf("group %q", group)
	}
	label := key
	if group == "round" {
		label = `coalesce(rp.full_name || '#' || rv.pr_number || ' ' || left(ro.head_sha, 7), ar.kind)`
	}
	rows, err := a.Pool.Query(ctx, `SELECT `+key+` AS k, min(`+label+`), sum(u.tokens_in), sum(u.tokens_out), sum(u.cost_usd)::float8
		FROM model_usage u JOIN agent_runs ar ON ar.id = u.run_id
		LEFT JOIN repositories rp ON rp.id = ar.repository_id
		LEFT JOIN rounds ro ON ro.id = ar.round_id LEFT JOIN reviews rv ON rv.id = ro.review_id
		GROUP BY k ORDER BY sum(u.cost_usd) DESC, k LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.Key, &r.Label, &r.TokensIn, &r.TokensOut, &r.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Register adds the steering and usage operations.
func (a *API) Register(api huma.API, view, edit map[string]any, webURL func(context.Context) string, actor func(context.Context) audit.Actor) {
	tags := []string{"steering"}
	huma.Register(api, huma.Operation{OperationID: "steering-queue", Method: http.MethodGet, Path: "/steering", Tags: tags, Metadata: edit},
		func(ctx context.Context, in *QueueFilter) (*struct{ Body []QueueItem }, error) {
			q, err := a.Queue(ctx, *in)
			return &struct{ Body []QueueItem }{q}, err
		})
	huma.Register(api, huma.Operation{OperationID: "save-steering-review", Method: http.MethodPut, Path: "/rounds/{id}/steering-review", Tags: tags, Metadata: edit},
		func(ctx context.Context, in *struct {
			ID   string `path:"id" format:"uuid"`
			Body ReviewInput
		}) (*struct{ Body SteeringReview }, error) {
			id, err := uuid.Parse(in.ID)
			if err != nil {
				return nil, huma.Error404NotFound("round not found")
			}
			who := auth.UserFrom(ctx)
			tagsIn := in.Body.Tags
			if tagsIn == nil {
				tagsIn = []string{}
			}
			var r SteeringReview
			err = pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `INSERT INTO steering_reviews (id, round_id, tags, note, author_login) VALUES ($1, $2, $3, $4, $5)
					ON CONFLICT (round_id) DO UPDATE SET tags = EXCLUDED.tags, note = EXCLUDED.note, author_login = EXCLUDED.author_login, created_at = now()
					RETURNING tags, note, author_login, created_at`, uuid.Must(uuid.NewV7()), id, tagsIn, in.Body.Note, who.Login).
					Scan(&r.Tags, &r.Note, &r.Author, &r.CreatedAt); err != nil {
					return err
				}
				return audit.Write(ctx, tx, actor(ctx), "steering.review", id.String(), map[string]any{"tags": tagsIn})
			})
			if err != nil && strings.Contains(err.Error(), "steering_reviews_round_id_fkey") {
				return nil, huma.Error404NotFound("round not found")
			}
			return &struct{ Body SteeringReview }{r}, err
		})
	huma.Register(api, huma.Operation{OperationID: "export-case", Method: http.MethodGet, Path: "/rounds/{id}/case", Tags: tags, Metadata: view},
		func(ctx context.Context, in *struct {
			ID string `path:"id" format:"uuid"`
		}) (*struct {
			ContentDisposition string `header:"Content-Disposition"`
			Body               CaseFile
		}, error) {
			id, err := uuid.Parse(in.ID)
			if err != nil {
				return nil, huma.Error404NotFound("round not found")
			}
			c, err := a.Case(ctx, id, webURL(ctx))
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("round not found")
			}
			return &struct {
				ContentDisposition string `header:"Content-Disposition"`
				Body               CaseFile
			}{fmt.Sprintf(`attachment; filename="sayso-case-%s.json"`, id), c}, err
		})
	huma.Register(api, huma.Operation{OperationID: "get-usage", Method: http.MethodGet, Path: "/usage", Tags: []string{"usage"}, Metadata: edit},
		func(ctx context.Context, in *struct {
			Group string `query:"group" enum:"round,repository,profile,month" default:"month"`
		}) (*struct{ Body []UsageRow }, error) {
			u, err := a.Usage(ctx, in.Group)
			return &struct{ Body []UsageRow }{u}, err
		})
}
