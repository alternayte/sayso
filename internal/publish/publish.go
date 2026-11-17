package publish

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	// AppLogin is the login of the App bot. Only its comments count as
	// Sayso's when Sayso looks for its markers.
	AppLogin func(ctx context.Context) string
}

// ours reports whether the App wrote a comment, so a marker that a person
// copies into their own comment takes nothing over.
func (p *Publisher) ours(ctx context.Context, login string) bool {
	if p.AppLogin != nil {
		if a := p.AppLogin(ctx); a != "" {
			return login == a
		}
	}
	return strings.HasSuffix(login, "[bot]")
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
	// Annotations are the check run annotations of the findings that have
	// no inline comment.
	Annotations []Annotation `json:"annotations"`
	// Unchallenged are the findings of the review stage of a failed run.
	// They are not in the ledger and are never posted.
	Unchallenged []runner.Finding `json:"unchallenged,omitempty"`
}

// Annotation is one check run annotation on the lines of a finding.
type Annotation struct {
	FindingID string `json:"finding_id"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Level     string `json:"level" enum:"failure,warning,notice"`
	Title     string `json:"title"`
	Message   string `json:"message"`
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
	Unchallenged                 []runner.Finding
}

func (p *Publisher) load(ctx context.Context, roundID uuid.UUID) (*round, error) {
	r := &round{ID: roundID}
	var verdict *string
	var assessment, outcome, config, skill, unchallenged []byte
	err := p.Pool.QueryRow(ctx, `SELECT ro.review_id, rv.repository_id, rv.pr_number, rp.full_name, rp.mode, rp.installation_id, ro.head_sha, ro.status,
			ro.failure_reason, ro.verdict, rv.state = 'paused',
			ro.assessment, ro.ledger_outcome, ro.config_snapshot, ro.skill_ref, ro.check_run_id, rv.summary_comment_id, ro.unchallenged
		FROM rounds ro JOIN reviews rv ON rv.id = ro.review_id JOIN repositories rp ON rp.id = rv.repository_id WHERE ro.id = $1`, roundID).
		Scan(&r.ReviewID, &r.RepositoryID, &r.Number, &r.FullName, &r.Mode, &r.Inst, &r.HeadSHA, &r.Status, &r.Reason, &verdict, &r.Paused,
			&assessment, &outcome, &config, &skill, &r.CheckRunID, &r.SummaryCommentID, &unchallenged)
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
	if len(unchallenged) > 0 {
		_ = json.Unmarshal(unchallenged, &r.Unchallenged)
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
		RoundURL: strings.TrimRight(p.BaseURL, "/") + "/rounds/" + r.ID.String(), ModelsURL: strings.TrimRight(p.BaseURL, "/") + "/settings/models",
		Skill: r.Skill, Profiles: r.Profiles}
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

// hunk is the right-side line range of one diff hunk.
type hunk struct{ start, end int }

// anchor places a finding range inside one hunk: the hunk that holds the
// end line, or else the last hunk that meets the range. GitHub refuses a
// review whose comment range crosses hunks. clipped reports a range that
// is smaller than the finding.
func anchor(hs []hunk, start, end int) (from, to int, clipped, ok bool) {
	for i := len(hs) - 1; i >= 0; i-- {
		h := hs[i]
		if h.start > end || h.end < start {
			continue
		}
		from, to = max(start, h.start), min(end, h.end)
		return from, to, from != start || to != end, true
	}
	return 0, 0, false, false
}

// annotationLevel maps a severity to a check run annotation level.
func annotationLevel(severity string) string {
	switch severity {
	case "blocker":
		return "failure"
	case "major":
		return "warning"
	}
	return "notice"
}

// fileLink is the URL of the lines of a finding at its commit.
func fileLink(web, fullName string, f findings.Finding) string {
	u := fmt.Sprintf("%s/%s/blob/%s/%s#L%d", web, fullName, f.CommitSHA, f.Path, f.StartLine)
	if f.EndLine > f.StartLine {
		u += fmt.Sprintf("-L%d", f.EndLine)
	}
	return u
}

// plan selects the inline comments, the annotations and the replies of a
// round (§10.3, §9.3, §9.4). An open finding that gets no inline comment
// gets an annotation on its lines. A nil diff is shadow mode.
func plan(r *round, ledger []findings.Finding, diff map[string][]hunk, limit int, web string) ([]Comment, []Reply, []Annotation, map[string]string) {
	byID := map[uuid.UUID]findings.Finding{}
	for _, f := range ledger {
		byID[f.ID] = f
	}
	links := map[string]string{}
	for _, f := range ledger {
		if f.CommentID != nil {
			links[f.ID.String()] = fmt.Sprintf("%s/%s/pull/%d#discussion_r%d", web, r.FullName, r.Number, *f.CommentID)
		} else {
			links[f.ID.String()] = fileLink(web, r.FullName, f)
		}
	}
	if r.Outcome == nil || r.Status != "done" {
		return nil, nil, nil, links
	}
	type candidate struct {
		f        findings.Finding
		from, to int
		clipped  bool
	}
	var candidates []candidate
	for _, id := range r.Outcome.New {
		f := byID[id]
		if f.Status != findings.StatusOpen || f.CommentID != nil {
			continue
		}
		if f.Severity == "nit" && r.Overlay.Nits != "inline" {
			continue
		}
		// Shadow mode reads no diff: every finding range counts as changed.
		from, to, clipped, ok := f.StartLine, f.EndLine, false, true
		if diff != nil {
			from, to, clipped, ok = anchor(diff[f.Path], f.StartLine, f.EndLine)
		}
		if !ok {
			continue
		}
		candidates = append(candidates, candidate{f, from, to, clipped})
	}
	slices.SortStableFunc(candidates, func(a, b candidate) int { return compareFindings(a.f, b.f) })
	var comments []Comment
	inline := map[uuid.UUID]bool{}
	for _, c := range candidates[:min(limit, len(candidates))] {
		cm := Comment{FindingID: c.f.ID.String(), Path: c.f.Path, Line: c.to, Body: RenderFinding(c.f, c.clipped)}
		if c.from < c.to {
			cm.StartLine = c.from
		}
		comments = append(comments, cm)
		inline[c.f.ID] = true
	}
	var annotations []Annotation
	open := slices.Clone(ledger)
	SortFindings(open)
	for _, f := range open {
		if f.Status != findings.StatusOpen || f.CommentID != nil || inline[f.ID] {
			continue
		}
		annotations = append(annotations, Annotation{FindingID: f.ID.String(), Path: f.Path, StartLine: f.StartLine, EndLine: f.EndLine,
			Level: annotationLevel(f.Severity), Title: clipText(f.Label()+" · "+severityText(f)+" · "+f.Title, 255), Message: clipText(cmp.Or(f.Body, f.Title), 60000)})
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
	return comments, replies, annotations, links
}

// clipText cuts s to at most n bytes on a rune boundary.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

// diffHunks returns, for each changed file, the right-side line ranges of
// its hunks, where a review comment can go.
func diffHunks(files []github.PullFile) map[string][]hunk {
	out := map[string][]hunk{}
	for _, f := range files {
		var hs []hunk
		line := 0
		for _, l := range strings.Split(f.Patch, "\n") {
			switch {
			case strings.HasPrefix(l, "@@"):
				// @@ -a,b +c,d @@
				line = 0
				if i := strings.Index(l, "+"); i >= 0 {
					rest := l[i+1:]
					if j := strings.IndexAny(rest, ", "); j >= 0 {
						rest = rest[:j]
					}
					line, _ = strconv.Atoi(rest)
				}
				if line > 0 {
					hs = append(hs, hunk{start: line, end: line - 1})
				}
			case strings.HasPrefix(l, "-"):
			case strings.HasPrefix(l, `\`):
			default:
				if line > 0 && len(hs) > 0 {
					hs[len(hs)-1].end = line
					line++
				}
			}
		}
		out[findings.NormalizePath(f.Filename)] = slices.DeleteFunc(hs, func(h hunk) bool { return h.end < h.start })
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
	var diff map[string][]hunk
	if !shadow {
		if gh, err = p.GitHub(ctx, r.Inst, r.FullName); err != nil {
			return pv, err
		}
		files, err := gh.Client.PullFiles(ctx, gh.Token, gh.Inst, r.FullName, r.Number)
		if err != nil {
			return pv, err
		}
		diff = diffHunks(files)
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
	comments, replies, annotations, links := plan(r, ledger, diff, limit, web)
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
	pv = Preview{Summary: RenderSummary(s), Comments: comments, Replies: replies, Check: &check, Annotations: annotations, Unchallenged: r.Unchallenged}
	if pv.Annotations == nil {
		pv.Annotations = []Annotation{}
	}
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
	// 4. The check run, with the override when there is one, and the
	// annotations of the findings without an inline comment.
	posted, err := p.putCheck(ctx, gh, r, check)
	if err != nil {
		return pv, err
	}
	if err := p.annotate(ctx, gh, r, posted, annotations); err != nil {
		return pv, err
	}
	raw, _ = json.Marshal(pv)
	_, err = p.Pool.Exec(ctx, `UPDATE rounds SET preview = $2 WHERE id = $1`, roundID, raw)
	return pv, err
}

// markerID reads the last finding marker: Sayso writes it at the end, after
// any agent text that could hold a copy.
func markerID(body string) string {
	i := strings.LastIndex(body, "<!-- sayso:finding:")
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
		if id := markerID(c.Body); id != "" && c.InReplyTo == 0 && p.ours(ctx, c.User.Login) {
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
			if fid := markerID(c.Body); fid != "" && c.InReplyTo == 0 && p.ours(ctx, c.User.Login) {
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
			if strings.HasPrefix(c.Body, SummaryMarker) && p.ours(ctx, c.User.Login) {
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
	run.Output = &github.CheckOutput{Title: c.Title, Summary: c.Summary}
	return run
}

// putCheck creates or updates the round's check run, and returns the check
// it posted: the override wins when there is one.
func (p *Publisher) putCheck(ctx context.Context, gh Session, r *round, c Check) (Check, error) {
	var override []byte
	if err := p.Pool.QueryRow(ctx, `SELECT override FROM rounds WHERE id = $1`, r.ID).Scan(&override); err != nil {
		return c, err
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
		return c, err
	}
	run := checkOf(r, c)
	if id != 0 {
		run.ID = id
		return c, gh.Client.UpdateCheckRun(ctx, gh.Token, gh.Inst, r.FullName, run)
	}
	created, err := gh.Client.CreateCheckRun(ctx, gh.Token, gh.Inst, r.FullName, run)
	if err != nil {
		return c, err
	}
	r.CheckRunID = &created.ID
	_, err = p.Pool.Exec(ctx, `UPDATE rounds SET check_run_id = $2 WHERE id = $1`, r.ID, created.ID)
	return c, err
}

// annotationBatch is the most annotations GitHub takes in one request.
const annotationBatch = 50

// annotate adds the annotations to the round's check run, 50 per request.
// GitHub appends annotations on each update, so a retry skips the batches
// that the publications table records.
func (p *Publisher) annotate(ctx context.Context, gh Session, r *round, c Check, as []Annotation) error {
	if len(as) == 0 {
		return nil
	}
	id, err := p.checkRun(ctx, gh, r)
	if err != nil || id == 0 {
		return err
	}
	for i := 0; i < len(as); i += annotationBatch {
		key := fmt.Sprintf("annotations:%d", i/annotationBatch)
		ok, _, err := done(ctx, p.Pool, r.ID, key)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		run := checkOf(r, c)
		run.ID = id
		for _, a := range as[i:min(i+annotationBatch, len(as))] {
			run.Output.Annotations = append(run.Output.Annotations, github.Annotation{Path: a.Path, StartLine: a.StartLine, EndLine: a.EndLine,
				AnnotationLevel: a.Level, Title: a.Title, Message: a.Message})
		}
		if err := gh.Client.UpdateCheckRun(ctx, gh.Token, gh.Inst, r.FullName, run); err != nil {
			return err
		}
		if err := record(ctx, p.Pool, r.ID, key, id); err != nil {
			return err
		}
	}
	return nil
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
	if _, err := p.putCheck(ctx, gh, r, c); err != nil {
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
	_, _, _, links := plan(&round{FullName: r.FullName, Number: r.Number}, ledger, nil, 0, p.webURL(ctx))
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
	_, err = p.putCheck(ctx, gh, r, Check{Status: "completed", Conclusion: "success"})
	return err
}
