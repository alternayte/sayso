package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/findings"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/settings"
)

// CheckName is the name of the Sayso check run (§10.4).
const CheckName = "sayso"

// Session is a GitHub client with an installation token for one repository.
type Session struct {
	Client *github.Client
	Token  string
	Inst   int64
}

// Publisher writes rounds to GitHub.
type Publisher struct {
	Pool     *pgxpool.Pool
	Settings *settings.Store
	// GitHub returns a client with a token that may write to the repository.
	GitHub func(ctx context.Context, inst int64, fullName string) (Session, error)
	// BaseURL is the public URL of the UI, for round links.
	BaseURL string
}

// Outcome is the stored ledger outcome of a round (rounds.ledger_outcome).
type Outcome = findings.Outcome

// Comment is one inline comment that Publish posts.
type Comment struct {
	FindingID string `json:"finding_id"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line,omitempty"`
	Line      int    `json:"line"`
	Body      string `json:"body"`
}

// Reply is one reply in a finding thread.
type Reply struct {
	Key       string `json:"key"`
	FindingID string `json:"finding_id"`
	Body      string `json:"body"`
	// Resolve resolves the thread after the reply.
	Resolve bool `json:"resolve,omitempty"`
}

// Preview is what a round posts, rendered (§10.1 shadow mode).
type Preview struct {
	Summary  string    `json:"summary"`
	Comments []Comment `json:"comments"`
	Replies  []Reply   `json:"replies"`
	Check    *Check    `json:"check,omitempty"`
}

// round is what Publish reads about a round.
type round struct {
	ID, ReviewID, RepositoryID   uuid.UUID
	Number                       int
	FullName, Mode               string
	Inst                         int64
	HeadSHA, Status, Reason      string
	Verdict                      string
	Paused                       bool
	Assessment                   *runner.Assessment
	Outcome                      *Outcome
	Overlay                      runner.Overlay
	Skill                        *runner.SkillRef
	Profiles                     map[string]string
	CheckRunID, SummaryCommentID *int64
}

func (p *Publisher) load(ctx context.Context, roundID uuid.UUID) (*round, error) {
	r := &round{ID: roundID}
	var verdict *string
	var assessment, outcome, config, skill []byte
	err := p.Pool.QueryRow(ctx, `SELECT ro.review_id, rv.repository_id, rv.pr_number, rp.full_name, rp.mode, rp.installation_id, ro.head_sha, ro.status,
			ro.failure_reason, ro.verdict, rv.state = 'paused',
			ro.assessment, ro.ledger_outcome, ro.config_snapshot, ro.skill_ref, ro.check_run_id, rv.summary_comment_id
		FROM rounds ro JOIN reviews rv ON rv.id = ro.review_id JOIN repositories rp ON rp.id = rv.repository_id WHERE ro.id = $1`, roundID).
		Scan(&r.ReviewID, &r.RepositoryID, &r.Number, &r.FullName, &r.Mode, &r.Inst, &r.HeadSHA, &r.Status, &r.Reason, &verdict, &r.Paused,
			&assessment, &outcome, &config, &skill, &r.CheckRunID, &r.SummaryCommentID)
	if err != nil {
		return nil, err
	}
	if verdict != nil {
		r.Verdict = *verdict
	}
	// A publishing round has its ledger outcome; Publish renders it as done.
	if r.Status == "publishing" {
		r.Status = "done"
	}
	if len(assessment) > 0 {
		r.Assessment = &runner.Assessment{}
		_ = json.Unmarshal(assessment, r.Assessment)
	}
	if len(outcome) > 0 {
		r.Outcome = &Outcome{}
		_ = json.Unmarshal(outcome, r.Outcome)
	}
	var cfg struct {
		Overlay  runner.Overlay    `json:"overlay"`
		Profiles map[string]string `json:"profiles"`
	}
	cfg.Overlay.Block = true
	if len(config) > 0 {
		_ = json.Unmarshal(config, &cfg)
	}
	r.Overlay, r.Profiles = cfg.Overlay, cfg.Profiles
	if len(skill) > 0 {
		r.Skill = &runner.SkillRef{}
		_ = json.Unmarshal(skill, r.Skill)
	}
	return r, nil
}

func (p *Publisher) intSetting(ctx context.Context, key string, def int) int {
	v := def
	if err := p.Settings.Decode(ctx, key, &v); err != nil || v <= 0 {
		return def
	}
	return v
}

func (p *Publisher) webURL(ctx context.Context) string {
	u := "https://github.com"
	_ = p.Settings.Decode(ctx, settings.GitHubWebURL, &u)
	return strings.TrimRight(u, "/")
}

// summaryOf builds the summary of a review at a round. While a round runs,
// the content comes from the last done round.
func (p *Publisher) summaryOf(ctx context.Context, tx pgx.Tx, r *round, ledger []findings.Finding, links map[string]string) (Summary, error) {
	s := Summary{Status: r.Status, HeadSHA: r.HeadSHA, Reason: r.Reason, Paused: r.Paused, Verdict: r.Verdict, Assessment: r.Assessment, Links: links,
		RoundURL: strings.TrimRight(p.BaseURL, "/") + "/rounds/" + r.ID.String(), Skill: r.Skill, Profiles: r.Profiles}
	if r.Status != "done" {
		var verdict *string
		var a []byte
		err := tx.QueryRow(ctx, `SELECT verdict, assessment FROM rounds WHERE review_id = $1 AND status = 'done' AND id <> $2 ORDER BY finished_at DESC LIMIT 1`,
			r.ReviewID, r.ID).Scan(&verdict, &a)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return s, err
		}
		s.Verdict, s.Assessment = "", nil
		if verdict != nil && len(a) > 0 {
			s.Verdict, s.Assessment = *verdict, &runner.Assessment{}
			_ = json.Unmarshal(a, s.Assessment)
		}
	}
	byID := map[uuid.UUID]findings.Finding{}
	for _, f := range ledger {
		byID[f.ID] = f
		if f.Status == findings.StatusOpen {
			s.Open = append(s.Open, f)
		}
	}
	if r.Outcome != nil && r.Status == "done" {
		for _, id := range r.Outcome.Fixed {
			s.Fixed = append(s.Fixed, byID[id])
		}
		for _, id := range r.Outcome.Withdrawn {
			if f := byID[id]; f.FirstRoundID != r.ID {
				s.Withdrawn = append(s.Withdrawn, f)
			}
		}
		for _, x := range r.Outcome.Reopened {
			s.Reopened = append(s.Reopened, byID[x.FindingID])
		}
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT f.id FROM finding_events e JOIN findings f ON f.id = e.finding_id
		WHERE f.review_id = $1 AND e.type IN ('dismissed', 'conceded') AND e.at > coalesce((SELECT max(finished_at) FROM rounds WHERE review_id = $1 AND status = 'done' AND id <> $2), '-infinity')`,
		r.ReviewID, r.ID)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return s, err
		}
		s.Dismissed = append(s.Dismissed, byID[id])
	}
	return s, rows.Err()
}

// plan selects the inline comments and replies of a round (§10.3, §9.3, §9.4).
func plan(r *round, ledger []findings.Finding, diff map[string]map[int]bool, limit int, web string) ([]Comment, []Reply, map[string]string) {
	byID := map[uuid.UUID]findings.Finding{}
	for _, f := range ledger {
		byID[f.ID] = f
	}
	links := map[string]string{}
	for _, f := range ledger {
		if f.CommentID != nil {
			links[f.ID.String()] = fmt.Sprintf("%s/%s/pull/%d#discussion_r%d", web, r.FullName, r.Number, *f.CommentID)
		} else {
			links[f.ID.String()] = fmt.Sprintf("%s/%s/blob/%s/%s#L%d", web, r.FullName, f.CommitSHA, f.Path, f.StartLine)
		}
	}
	if r.Outcome == nil || r.Status != "done" {
		return nil, nil, links
	}
	var candidates []findings.Finding
	for _, id := range r.Outcome.New {
		f := byID[id]
		if f.Status != findings.StatusOpen || f.CommentID != nil {
			continue
		}
		if f.Severity == "nit" && r.Overlay.Nits != "inline" {
			continue
		}
		if !diff[f.Path][f.EndLine] || (f.StartLine != f.EndLine && !diff[f.Path][f.StartLine]) {
			continue
		}
		candidates = append(candidates, f)
	}
	SortFindings(candidates)
	var comments []Comment
	for _, f := range candidates[:min(limit, len(candidates))] {
		c := Comment{FindingID: f.ID.String(), Path: f.Path, Line: f.EndLine, Body: RenderFinding(f)}
		if f.StartLine < f.EndLine {
			c.StartLine = f.StartLine
		}
		comments = append(comments, c)
	}
	var replies []Reply
	for _, id := range r.Outcome.Fixed {
		if f := byID[id]; f.CommentID != nil {
			replies = append(replies, Reply{Key: "fixed:" + id.String(), FindingID: id.String(), Body: "Fixed in " + sha7(r.HeadSHA) + ".", Resolve: true})
		}
	}
	for _, x := range r.Outcome.Reopened {
		if f := byID[x.FindingID]; f.CommentID != nil {
			body := fmt.Sprintf("**%s** is open again. A new test shows the issue: %s.\n\n%s", f.Label(), Code(x.Evidence.TestPath), Safe(x.Evidence.Detail))
			replies = append(replies, Reply{Key: "reopened:" + x.FindingID.String(), FindingID: x.FindingID.String(), Body: body})
		}
	}
	return comments, replies, links
}

// diffLines returns, for each changed file, the lines on the right side of
// the diff that a review comment can use.
func diffLines(files []github.PullFile) map[string]map[int]bool {
	out := map[string]map[int]bool{}
	for _, f := range files {
		lines := map[int]bool{}
		line := 0
		for _, l := range strings.Split(f.Patch, "\n") {
			switch {
			case strings.HasPrefix(l, "@@"):
				// @@ -a,b +c,d @@
				if i := strings.Index(l, "+"); i >= 0 {
					rest := l[i+1:]
					if j := strings.IndexAny(rest, ", "); j >= 0 {
						rest = rest[:j]
					}
					n, _ := strconv.Atoi(rest)
					line = n
				}
			case strings.HasPrefix(l, "-"):
			case strings.HasPrefix(l, `\`):
			default:
				if line > 0 {
					lines[line] = true
					line++
				}
			}
		}
		out[findings.NormalizePath(f.Filename)] = lines
	}
	return out
}

// done reads and records publications, so a retry skips finished writes.
func done(ctx context.Context, pool *pgxpool.Pool, roundID uuid.UUID, key string) (bool, int64, error) {
	var id *int64
	err := pool.QueryRow(ctx, `SELECT github_id FROM publications WHERE round_id = $1 AND key = $2`, roundID, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, 0, nil
	}
	if id == nil {
		return true, 0, err
	}
	return true, *id, err
}

func record(ctx context.Context, pool *pgxpool.Pool, roundID uuid.UUID, key string, id int64) error {
	_, err := pool.Exec(ctx, `INSERT INTO publications (round_id, key, github_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, roundID, key, id)
	return err
}

// Publish writes a finished round (§10). It is safe to retry: it finds
// earlier writes by the publications table and by markers (§10.5). In
// shadow mode it only stores the preview.
func (p *Publisher) Publish(ctx context.Context, roundID uuid.UUID) (Preview, error) {
	var pv Preview
	r, err := p.load(ctx, roundID)
	if err != nil {
		return pv, err
	}
	shadow := r.Mode == "shadow" || r.Mode == "off"
	var gh Session
	var diff map[string]map[int]bool
	if !shadow {
		if gh, err = p.GitHub(ctx, r.Inst, r.FullName); err != nil {
			return pv, err
		}
		files, err := gh.Client.PullFiles(ctx, gh.Token, gh.Inst, r.FullName, r.Number)
		if err != nil {
			return pv, err
		}
		diff = diffLines(files)
	} else if diff, err = p.shadowDiff(ctx, r); err != nil {
		return pv, err
	}
	web := p.webURL(ctx)
	limit := p.intSetting(ctx, settings.MaxInlineComments, 10)

	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return pv, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ledger, err := findings.List(ctx, tx, r.ReviewID)
	if err != nil {
		return pv, err
	}
	comments, replies, links := plan(r, ledger, diff, limit, web)
	s, err := p.summaryOf(ctx, tx, r, ledger, links)
	if err != nil {
		return pv, err
	}
	_ = tx.Rollback(ctx)
	var open []findings.Finding
	for _, f := range ledger {
		if f.Status == findings.StatusOpen {
			open = append(open, f)
		}
	}
	check := RenderCheck(r.Mode, r.Status, r.Verdict, r.Reason, r.Assessment, open)
	pv = Preview{Summary: RenderSummary(s), Comments: comments, Replies: replies, Check: &check}
	if pv.Comments == nil {
		pv.Comments = []Comment{}
	}
	if pv.Replies == nil {
		pv.Replies = []Reply{}
	}
	raw, _ := json.Marshal(pv)
	if _, err := p.Pool.Exec(ctx, `UPDATE rounds SET preview = $2 WHERE id = $1`, roundID, raw); err != nil {
		return pv, err
	}
	if shadow {
		return pv, nil
	}

	// 1. The review with the inline comments, once per round.
	if len(comments) > 0 {
		if err := p.postReview(ctx, gh, r, comments); err != nil {
			return pv, err
		}
	}
	// 2. Replies and thread states.
	if err := p.postReplies(ctx, gh, r, replies); err != nil {
		return pv, err
	}
	// 3. The summary, after the comments, so its links point at them.
	if err := p.refreshLinks(ctx, r, &s, web); err != nil {
		return pv, err
	}
	pv.Summary = RenderSummary(s)
	if err := p.putSummary(ctx, gh, r, pv.Summary, github.High); err != nil {
		return pv, err
	}
	// 4. The check run, with the override when there is one.
	if err := p.putCheck(ctx, gh, r, check); err != nil {
		return pv, err
	}
	raw, _ = json.Marshal(pv)
	_, err = p.Pool.Exec(ctx, `UPDATE rounds SET preview = $2 WHERE id = $1`, roundID, raw)
	return pv, err
}

// shadowDiff is the diff map in shadow mode, where Publish makes no GitHub
// calls: every finding line counts as in the diff.
func (p *Publisher) shadowDiff(ctx context.Context, r *round) (map[string]map[int]bool, error) {
	rows, err := p.Pool.Query(ctx, `SELECT path, start_line, end_line FROM findings WHERE review_id = $1`, r.ReviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[int]bool{}
	for rows.Next() {
		var path string
		var a, b int
		if err := rows.Scan(&path, &a, &b); err != nil {
			return nil, err
		}
		if out[path] == nil {
			out[path] = map[int]bool{}
		}
		out[path][a], out[path][b] = true, true
	}
	return out, rows.Err()
}

func markerID(body string) string {
	i := strings.Index(body, "<!-- sayso:finding:")
	if i < 0 {
		return ""
	}
	rest := body[i+len("<!-- sayso:finding:"):]
	j := strings.Index(rest, " -->")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// postReview posts one COMMENT review. Before it posts, it looks for
// comments with the markers of this round, which an earlier attempt may
// have posted.
func (p *Publisher) postReview(ctx context.Context, gh Session, r *round, comments []Comment) error {
	posted, _, err := done(ctx, p.Pool, r.ID, "review")
	if err != nil {
		return err
	}
	existing, err := gh.Client.PullComments(ctx, gh.Token, gh.Inst, r.FullName, r.Number)
	if err != nil {
		return err
	}
	byFinding := map[string]github.PullComment{}
	for _, c := range existing {
		if id := markerID(c.Body); id != "" && c.InReplyTo == 0 {
			byFinding[id] = c
		}
	}
	missing := 0
	for _, c := range comments {
		if _, ok := byFinding[c.FindingID]; !ok {
			missing++
		}
	}
	if !posted && missing == len(comments) {
		inline := make([]github.InlineComment, 0, len(comments))
		for _, c := range comments {
			inline = append(inline, github.InlineComment{Path: c.Path, Line: c.Line, StartLine: c.StartLine, Side: "RIGHT", Body: c.Body})
		}
		id, err := gh.Client.CreateCommentReview(ctx, gh.Token, gh.Inst, r.FullName, r.Number, r.HeadSHA, "", inline)
		if err != nil {
			return err
		}
		if err := record(ctx, p.Pool, r.ID, "review", id); err != nil {
			return err
		}
		if existing, err = gh.Client.PullComments(ctx, gh.Token, gh.Inst, r.FullName, r.Number); err != nil {
			return err
		}
		for _, c := range existing {
			if fid := markerID(c.Body); fid != "" && c.InReplyTo == 0 {
				byFinding[fid] = c
			}
		}
	}
	for _, c := range comments {
		pc, ok := byFinding[c.FindingID]
		if !ok {
			continue
		}
		thread, err := gh.Client.ReviewThreadID(ctx, gh.Token, gh.Inst, r.FullName, r.Number, pc.ID)
		if err != nil {
			return err
		}
		if _, err := p.Pool.Exec(ctx, `UPDATE findings SET comment_id = $2, thread_node_id = $3 WHERE id = $1`, uuid.MustParse(c.FindingID), pc.ID, thread); err != nil {
			return err
		}
	}
	return nil
}

func (p *Publisher) postReplies(ctx context.Context, gh Session, r *round, replies []Reply) error {
	for _, rp := range replies {
		var commentID *int64
		var thread string
		if err := p.Pool.QueryRow(ctx, `SELECT comment_id, thread_node_id FROM findings WHERE id = $1`, uuid.MustParse(rp.FindingID)).Scan(&commentID, &thread); err != nil {
			return err
		}
		if commentID == nil {
			continue
		}
		ok, _, err := done(ctx, p.Pool, r.ID, "reply:"+rp.Key)
		if err != nil {
			return err
		}
		if !ok {
			c, err := gh.Client.ReplyToComment(ctx, gh.Token, gh.Inst, r.FullName, r.Number, *commentID, rp.Body)
			if err != nil {
				return err
			}
			if err := record(ctx, p.Pool, r.ID, "reply:"+rp.Key, c.ID); err != nil {
				return err
			}
		}
		if rp.Resolve && thread != "" {
			if err := gh.Client.SetThreadResolved(ctx, gh.Token, gh.Inst, thread, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Publisher) refreshLinks(ctx context.Context, r *round, s *Summary, web string) error {
	rows, err := p.Pool.Query(ctx, `SELECT id, comment_id FROM findings WHERE review_id = $1 AND comment_id IS NOT NULL`, r.ReviewID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var c int64
		if err := rows.Scan(&id, &c); err != nil {
			return err
		}
		s.Links[id.String()] = fmt.Sprintf("%s/%s/pull/%d#discussion_r%d", web, r.FullName, r.Number, c)
	}
	return rows.Err()
}

// putSummary edits the summary comment in place, or creates it once.
func (p *Publisher) putSummary(ctx context.Context, gh Session, r *round, body string, prio github.Priority) error {
	id := r.SummaryCommentID
	if id == nil {
		cs, err := gh.Client.IssueComments(ctx, gh.Token, gh.Inst, r.FullName, r.Number)
		if err != nil {
			return err
		}
		for _, c := range cs {
			if strings.HasPrefix(c.Body, SummaryMarker) {
				id = &c.ID
				break
			}
		}
	}
	if id != nil {
		if err := gh.Client.EditIssueComment(ctx, gh.Token, gh.Inst, r.FullName, *id, body, prio); err != nil {
			return err
		}
	} else {
		c, err := gh.Client.CreateIssueComment(ctx, gh.Token, gh.Inst, r.FullName, r.Number, body, prio)
		if err != nil {
			return err
		}
		id = &c.ID
	}
	r.SummaryCommentID = id
	_, err := p.Pool.Exec(ctx, `UPDATE reviews SET summary_comment_id = $2 WHERE id = $1`, r.ReviewID, *id)
	return err
}

// checkRun finds the round's check run by its stored ID or its external ID.
func (p *Publisher) checkRun(ctx context.Context, gh Session, r *round) (int64, error) {
	if r.CheckRunID != nil {
		return *r.CheckRunID, nil
	}
	runs, err := gh.Client.CheckRunsFor(ctx, gh.Token, gh.Inst, r.FullName, r.HeadSHA, CheckName)
	if err != nil {
		return 0, err
	}
	for _, c := range runs {
		if c.ExternalID == r.ID.String() {
			_, err := p.Pool.Exec(ctx, `UPDATE rounds SET check_run_id = $2 WHERE id = $1`, r.ID, c.ID)
			return c.ID, err
		}
	}
	return 0, nil
}

func checkOf(r *round, c Check) github.CheckRun {
	run := github.CheckRun{Name: CheckName, HeadSHA: r.HeadSHA, ExternalID: r.ID.String(), Status: c.Status, Conclusion: c.Conclusion}
	run.Output = &struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}{c.Title, c.Summary}
	return run
}

func (p *Publisher) putCheck(ctx context.Context, gh Session, r *round, c Check) error {
	var override []byte
	if err := p.Pool.QueryRow(ctx, `SELECT override FROM rounds WHERE id = $1`, r.ID).Scan(&override); err != nil {
		return err
	}
	var ov struct {
		Login  string `json:"login"`
		Reason string `json:"reason"`
	}
	if len(override) > 0 && string(override) != "null" && json.Unmarshal(override, &ov) == nil && ov.Login != "" {
		c = Check{Status: "completed", Conclusion: "success", Title: "Overridden by @" + ov.Login, Summary: Safe(ov.Reason)}
	}
	id, err := p.checkRun(ctx, gh, r)
	if err != nil {
		return err
	}
	run := checkOf(r, c)
	if id != 0 {
		run.ID = id
		return gh.Client.UpdateCheckRun(ctx, gh.Token, gh.Inst, r.FullName, run)
	}
	created, err := gh.Client.CreateCheckRun(ctx, gh.Token, gh.Inst, r.FullName, run)
	if err != nil {
		return err
	}
	_, err = p.Pool.Exec(ctx, `UPDATE rounds SET check_run_id = $2 WHERE id = $1`, r.ID, created.ID)
	return err
}

// MarkStarted shows a started round on GitHub (§7.2 step 2, REQ-PUB-006):
// an in-progress check, the status line and the 👀 reaction. Shadow mode
// writes nothing.
func (p *Publisher) MarkStarted(ctx context.Context, roundID uuid.UUID) error {
	r, err := p.load(ctx, roundID)
	if err != nil || r.Mode == "shadow" || r.Mode == "off" {
		return err
	}
	gh, err := p.GitHub(ctx, r.Inst, r.FullName)
	if err != nil {
		return err
	}
	c := Check{Status: "in_progress", Title: "Reviewing " + sha7(r.HeadSHA), Summary: "Sayso is reviewing this commit."}
	if err := p.putCheck(ctx, gh, r, c); err != nil {
		return err
	}
	if err := p.StatusLine(ctx, roundID); err != nil {
		return err
	}
	return gh.Client.AddReaction(ctx, gh.Token, gh.Inst, r.FullName, r.Number, "eyes")
}

// StatusLine renders the summary with the current state of the round,
// without inline comments. Use it for running, paused and cancelled states.
func (p *Publisher) StatusLine(ctx context.Context, roundID uuid.UUID) error {
	r, err := p.load(ctx, roundID)
	if err != nil || r.Mode == "shadow" || r.Mode == "off" {
		return err
	}
	gh, err := p.GitHub(ctx, r.Inst, r.FullName)
	if err != nil {
		return err
	}
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ledger, err := findings.List(ctx, tx, r.ReviewID)
	if err != nil {
		return err
	}
	_, _, links := plan(&round{FullName: r.FullName, Number: r.Number}, ledger, nil, 0, p.webURL(ctx))
	s, err := p.summaryOf(ctx, tx, r, ledger, links)
	if err != nil {
		return err
	}
	prio := github.Low
	if r.Status != "running" && r.Status != "queued" {
		prio = github.High
	}
	return p.putSummary(ctx, gh, r, RenderSummary(s), prio)
}

// Override sets the round's check to success with the override (§10.4,
// §11.2). The round must already store the override.
func (p *Publisher) Override(ctx context.Context, roundID uuid.UUID) error {
	r, err := p.load(ctx, roundID)
	if err != nil || r.Mode == "shadow" || r.Mode == "off" {
		return err
	}
	gh, err := p.GitHub(ctx, r.Inst, r.FullName)
	if err != nil {
		return err
	}
	return p.putCheck(ctx, gh, r, Check{Status: "completed", Conclusion: "success"})
}
