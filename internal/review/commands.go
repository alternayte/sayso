package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/findings"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/publish"
	"github.com/alternayte/sayso/internal/settings"
)

// CommentInput names the comment of a command, reply or human comment signal.
type CommentInput struct {
	ReviewID  uuid.UUID `json:"review_id"`
	CommentID int64     `json:"comment_id"`
	Source    string    `json:"source"`
	InReplyTo int64     `json:"in_reply_to,omitempty"`
}

// Outcome actions that the PRReview workflow carries out.
const (
	ActionNone   = ""
	ActionRound  = "round"
	ActionPause  = "pause"
	ActionResume = "resume"
	ActionWork   = "work"
)

// CommandOutcome tells the workflow what to do after a command or reply.
type CommandOutcome struct {
	Action string     `json:"action,omitempty"`
	WorkID *uuid.UUID `json:"work_id,omitempty"`
}

// MaxThreadReplies is the number of Sayso replies in a thread between pushes (§11.3).
const MaxThreadReplies = 3

// StopMessage ends a thread until the next push.
const StopMessage = "I will look again after the next push."

type prRef struct {
	reviewID, repoID uuid.UUID
	full             string
	repoGitHubID     int64
	inst             int64
	number           int
	head             string
	gh               publish.Session
}

func (a *Activities) pr(ctx context.Context, reviewID uuid.UUID) (*prRef, error) {
	p := &prRef{reviewID: reviewID}
	err := a.Pool.QueryRow(ctx, `SELECT rp.id, rp.full_name, rp.github_id, rp.installation_id, rv.pr_number, rv.head_sha FROM reviews rv
		JOIN repositories rp ON rp.id = rv.repository_id WHERE rv.id = $1`, reviewID).Scan(&p.repoID, &p.full, &p.repoGitHubID, &p.inst, &p.number, &p.head)
	if err != nil {
		return nil, err
	}
	p.gh, err = a.GitHub(ctx, p.inst, p.full)
	return p, err
}

type comment struct {
	id, inReplyTo int64
	source        string
	author, body  string
	path          string
	line          int
}

func (a *Activities) comment(ctx context.Context, p *prRef, in CommentInput) (comment, error) {
	c := comment{id: in.CommentID, source: in.Source}
	if in.Source == "issue" {
		ic, err := p.gh.Client.IssueComment(ctx, p.gh.Token, p.gh.Inst, p.full, in.CommentID)
		c.author, c.body = ic.User.Login, ic.Body
		return c, err
	}
	pc, err := p.gh.Client.PullComment(ctx, p.gh.Token, p.gh.Inst, p.full, in.CommentID)
	c.author, c.body, c.path, c.line, c.inReplyTo = pc.User.Login, pc.Body, pc.Path, pc.Line, pc.InReplyTo
	return c, err
}

// answer replies to a comment: in its thread for a review comment, as a
// conversation comment otherwise.
func (a *Activities) answer(ctx context.Context, p *prRef, c comment, text string) error {
	if c.source == "review" {
		top := c.id
		if c.inReplyTo != 0 {
			top = c.inReplyTo
		}
		_, err := p.gh.Client.ReplyToComment(ctx, p.gh.Token, p.gh.Inst, p.full, p.number, top, text)
		return err
	}
	_, err := p.gh.Client.CreateIssueComment(ctx, p.gh.Token, p.gh.Inst, p.full, p.number, text, github.High)
	return err
}

func (a *Activities) mayOverride(ctx context.Context, p *prRef, login, permission string) (bool, error) {
	if Allowed("override", permission) {
		return true, nil
	}
	var teams []string
	if err := a.Settings.Decode(ctx, settings.OverrideTeams, &teams); err != nil || len(teams) == 0 {
		return false, err
	}
	org, _, _ := strings.Cut(p.full, "/")
	for _, team := range teams {
		ok, err := p.gh.Client.IsTeamMember(ctx, p.gh.Token, org, team, login)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func (a *Activities) findingBySeq(ctx context.Context, reviewID uuid.UUID, seq int) (*findings.Finding, error) {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	fs, err := findings.List(ctx, tx, reviewID)
	if err != nil {
		return nil, err
	}
	for i := range fs {
		if fs[i].Seq == seq {
			return &fs[i], nil
		}
	}
	return nil, nil
}

// dismiss sets a finding dismissed, resolves its thread and writes the signal (§11.1).
func (a *Activities) dismiss(ctx context.Context, p *prRef, f *findings.Finding, login, reason string) error {
	err := pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		if err := findings.SetStatus(ctx, tx, f.ID, nil, findings.StatusDismissed, "dismissed", login, reason); err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]string{"finding": f.Label(), "reason": reason})
		_, err := tx.Exec(ctx, `INSERT INTO steering_signals (id, review_id, round_id, finding_id, type, actor_login, detail)
			VALUES ($1, $2, $3, $4, 'dismissed', $5, $6)`, uuid.Must(uuid.NewV7()), p.reviewID, f.LastRoundID, f.ID, login, detail)
		return err
	})
	if err != nil || f.ThreadNodeID == "" {
		return err
	}
	return p.gh.Client.SetThreadResolved(ctx, p.gh.Token, p.gh.Inst, f.ThreadNodeID, true)
}

func (a *Activities) queueWork(ctx context.Context, p *prRef, kind string, f *findings.Finding, c comment, action string) (*uuid.UUID, error) {
	id := uuid.Must(uuid.NewV7())
	_, err := a.Pool.Exec(ctx, `INSERT INTO agent_work (id, review_id, kind, finding_id, comment_id, source, action, actor_login, head_sha) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, p.reviewID, kind, f.ID, c.id, c.source, action, c.author, p.head)
	return &id, err
}

// HandleCommand checks the commenter's permission and runs a /sayso
// command (§11.1, SI-08). Commands that need no agent act at once; fix and
// explain queue a run for the workflow.
func (a *Activities) HandleCommand(ctx context.Context, in CommentInput) (CommandOutcome, error) {
	p, err := a.pr(ctx, in.ReviewID)
	if err != nil {
		return CommandOutcome{}, err
	}
	c, err := a.comment(ctx, p, in)
	if err != nil {
		return CommandOutcome{}, err
	}
	cmd := ParseCommand(c.body)
	refuse := func(why string) (CommandOutcome, error) {
		return CommandOutcome{}, a.answer(ctx, p, c, "Sayso did not run this command: "+why+".")
	}
	if cmd.Err != "" {
		return refuse(cmd.Err)
	}
	perm, err := p.gh.Client.Permission(ctx, p.gh.Token, p.gh.Inst, p.full, c.author)
	if err != nil {
		return CommandOutcome{}, err
	}
	allowed := Allowed(cmd.Name, perm)
	if cmd.Name == "override" {
		if allowed, err = a.mayOverride(ctx, p, c.author, perm); err != nil {
			return CommandOutcome{}, err
		}
	}
	if !allowed {
		return refuse(fmt.Sprintf("/sayso %s needs %s permission on %s, and you have %s", cmd.Name, needs[cmd.Name], p.full, perm))
	}
	var f *findings.Finding
	if cmd.Finding > 0 {
		if f, err = a.findingBySeq(ctx, p.reviewID, cmd.Finding); err != nil {
			return CommandOutcome{}, err
		}
		if f == nil {
			return refuse(fmt.Sprintf("this pull request has no finding S-%d", cmd.Finding))
		}
	}
	out := CommandOutcome{}
	switch cmd.Name {
	case "review":
		out.Action = ActionRound
		// A person asks for a new round: a steering signal (§5).
		detail, _ := json.Marshal(map[string]string{"head_sha": p.head, "comment": fmt.Sprint(c.id)})
		if _, err := a.Pool.Exec(ctx, `INSERT INTO steering_signals (id, review_id, round_id, type, actor_login, detail)
			VALUES ($1, $2, (SELECT id FROM rounds WHERE review_id = $2 ORDER BY created_at DESC LIMIT 1), 'rerun_requested', $3, $4)`,
			uuid.Must(uuid.NewV7()), p.reviewID, c.author, detail); err != nil {
			return out, err
		}
	case "pause":
		out.Action = ActionPause
	case "resume":
		out.Action = ActionResume
	case "dismiss":
		if err := a.dismiss(ctx, p, f, c.author, cmd.Reason); err != nil {
			return out, err
		}
	case "override":
		if err := a.override(ctx, p, c.author, cmd.Reason); err != nil {
			if errors.Is(err, errNoRound) {
				return refuse("the head commit has no round to override")
			}
			return out, err
		}
	case "fix":
		pr, err := p.gh.Client.GetPull(ctx, p.gh.Token, p.gh.Inst, p.full, p.number)
		if err != nil {
			return out, err
		}
		if pr.Head.Repo == nil || pr.Head.Repo.ID != p.repoGitHubID {
			return refuse("/sayso fix works only for a branch in this repository, and this pull request comes from a fork")
		}
		if out.WorkID, err = a.queueWork(ctx, p, KindFix, f, c, ""); err != nil {
			return out, err
		}
		out.Action = ActionWork
	case "explain":
		if out.WorkID, err = a.queueWork(ctx, p, KindReply, f, c, "clarify"); err != nil {
			return out, err
		}
		out.Action = ActionWork
	}
	return out, p.gh.Client.ReactToComment(ctx, p.gh.Token, p.gh.Inst, p.full, c.source, c.id, "+1")
}

var errNoRound = errors.New("no round for the head commit")

// override sets the check of the current head to success (§11.2).
func (a *Activities) override(ctx context.Context, p *prRef, login, reason string) error {
	var roundID uuid.UUID
	err := a.Pool.QueryRow(ctx, `SELECT id FROM rounds WHERE review_id = $1 AND head_sha = $2 ORDER BY created_at DESC LIMIT 1`, p.reviewID, p.head).Scan(&roundID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNoRound
	}
	if err != nil {
		return err
	}
	ov, _ := json.Marshal(map[string]any{"login": login, "reason": reason, "at": time.Now().UTC()})
	err = pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE rounds SET override = $2 WHERE id = $1`, roundID, ov); err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]string{"reason": reason, "head_sha": p.head})
		if _, err := tx.Exec(ctx, `INSERT INTO steering_signals (id, review_id, round_id, type, actor_login, detail) VALUES ($1, $2, $3, 'overridden', $4, $5)`,
			uuid.Must(uuid.NewV7()), p.reviewID, roundID, login, detail); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.System, "round.override", fmt.Sprintf("%s#%d %s", p.full, p.number, p.head), map[string]any{"login": login, "reason": reason})
	})
	if err != nil {
		return err
	}
	return a.Publisher.Override(ctx, roundID)
}

// HandleThreadReply handles a person's reply in a Sayso thread (§11.3).
func (a *Activities) HandleThreadReply(ctx context.Context, in CommentInput) (CommandOutcome, error) {
	p, err := a.pr(ctx, in.ReviewID)
	if err != nil {
		return CommandOutcome{}, err
	}
	c, err := a.comment(ctx, p, in)
	if err != nil {
		return CommandOutcome{}, err
	}
	var fid uuid.UUID
	err = a.Pool.QueryRow(ctx, `SELECT id FROM findings WHERE review_id = $1 AND comment_id = $2`, p.reviewID, c.inReplyTo).Scan(&fid)
	if errors.Is(err, pgx.ErrNoRows) {
		return CommandOutcome{}, nil
	}
	if err != nil {
		return CommandOutcome{}, err
	}
	var f *findings.Finding
	var seq int
	if err := a.Pool.QueryRow(ctx, `SELECT seq FROM findings WHERE id = $1`, fid).Scan(&seq); err != nil {
		return CommandOutcome{}, err
	}
	if f, err = a.findingBySeq(ctx, p.reviewID, seq); err != nil {
		return CommandOutcome{}, err
	}
	if rest, ok := strings.CutPrefix(strings.TrimSpace(c.body), "dismiss:"); ok {
		perm, err := p.gh.Client.Permission(ctx, p.gh.Token, p.gh.Inst, p.full, c.author)
		if err != nil {
			return CommandOutcome{}, err
		}
		if !Allowed("dismiss", perm) {
			return CommandOutcome{}, a.answer(ctx, p, c, fmt.Sprintf("Sayso did not dismiss this finding: it needs write permission on %s.", p.full))
		}
		reason := strings.TrimSpace(rest)
		if reason == "" {
			return CommandOutcome{}, a.answer(ctx, p, c, "Sayso did not dismiss this finding: give a reason after dismiss:.")
		}
		if err := a.dismiss(ctx, p, f, c.author, reason); err != nil {
			return CommandOutcome{}, err
		}
		return CommandOutcome{}, p.gh.Client.ReactToComment(ctx, p.gh.Token, p.gh.Inst, p.full, c.source, c.id, "+1")
	}
	// At most 3 replies in one thread without a new push.
	var replies int
	if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_work WHERE finding_id = $1 AND kind = 'reply' AND head_sha = $2 AND status <> 'cancelled'`,
		f.ID, p.head).Scan(&replies); err != nil {
		return CommandOutcome{}, err
	}
	if replies >= MaxThreadReplies {
		var stopped bool
		if err := a.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent_work WHERE finding_id = $1 AND head_sha = $2 AND status = 'cancelled' AND reason = 'reply limit')`,
			f.ID, p.head).Scan(&stopped); err != nil || stopped {
			return CommandOutcome{}, err
		}
		if _, err := a.Pool.Exec(ctx, `INSERT INTO agent_work (id, review_id, kind, finding_id, comment_id, source, actor_login, head_sha, status, reason, finished_at)
			VALUES ($1, $2, 'reply', $3, $4, $5, $6, $7, 'cancelled', 'reply limit', now())`, uuid.Must(uuid.NewV7()), p.reviewID, f.ID, c.id, c.source, c.author, p.head); err != nil {
			return CommandOutcome{}, err
		}
		return CommandOutcome{}, a.answer(ctx, p, c, StopMessage)
	}
	id, err := a.queueWork(ctx, p, KindReply, f, c, "")
	return CommandOutcome{Action: ActionWork, WorkID: id}, err
}

// HandleHumanComment records a person's comment on a line that no open or
// recent Sayso finding covers (§11.4). Sayso does not answer it.
func (a *Activities) HandleHumanComment(ctx context.Context, in CommentInput) error {
	if in.InReplyTo != 0 {
		return nil
	}
	p, err := a.pr(ctx, in.ReviewID)
	if err != nil {
		return err
	}
	c, err := a.comment(ctx, p, in)
	if err != nil || c.path == "" {
		return err
	}
	var covered bool
	if err := a.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM findings f WHERE f.review_id = $1 AND f.path = $2 AND $3 BETWEEN f.start_line AND f.end_line
		AND (f.status = 'open' OR f.updated_at > now() - interval '7 days'))`, p.reviewID, findings.NormalizePath(c.path), c.line).Scan(&covered); err != nil || covered {
		return err
	}
	excerpt := c.body
	if r := []rune(excerpt); len(r) > 300 {
		excerpt = string(r[:300])
	}
	detail, _ := json.Marshal(map[string]any{"path": c.path, "line": c.line, "author": c.author, "excerpt": excerpt, "comment_id": c.id})
	_, err = a.Pool.Exec(ctx, `INSERT INTO steering_signals (id, review_id, type, actor_login, detail) VALUES ($1, $2, 'human_comment_unflagged', $3, $4)`,
		uuid.Must(uuid.NewV7()), p.reviewID, c.author, detail)
	return err
}
