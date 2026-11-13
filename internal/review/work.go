package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/findings"
	"github.com/alternayte/sayso/internal/publish"
	"github.com/alternayte/sayso/internal/runner"
)

// WorkInput names a queued reply or fix run.
type WorkInput struct {
	WorkID       uuid.UUID `json:"work_id"`
	RepositoryID uuid.UUID `json:"repository_id"`
	Kind         string    `json:"kind"`
}

type work struct {
	id, reviewID, findingID uuid.UUID
	kind, action, source    string
	commentID               int64
	head                    string
}

func (a *Activities) work(ctx context.Context, id uuid.UUID) (work, error) {
	w := work{id: id}
	err := a.Pool.QueryRow(ctx, `SELECT review_id, finding_id, kind, action, source, comment_id, head_sha FROM agent_work WHERE id = $1`, id).
		Scan(&w.reviewID, &w.findingID, &w.kind, &w.action, &w.source, &w.commentID, &w.head)
	return w, err
}

func (a *Activities) finishWork(ctx context.Context, id uuid.UUID, status, reason string, outcome any) error {
	var raw []byte
	if outcome != nil {
		raw, _ = json.Marshal(outcome)
	}
	_, err := a.Pool.Exec(ctx, `UPDATE agent_work SET status = $2, reason = $3, outcome = $4, finished_at = now() WHERE id = $1`, id, status, reason, raw)
	return err
}

// PrepareWork builds the spec of a reply or fix run from the review's last
// round and the finding's thread (§11.3).
func (a *Activities) PrepareWork(ctx context.Context, id uuid.UUID) error {
	w, err := a.work(ctx, id)
	if err != nil {
		return err
	}
	var cfgRaw []byte
	err = a.Pool.QueryRow(ctx, `SELECT config_snapshot FROM rounds WHERE review_id = $1 AND config_snapshot IS NOT NULL ORDER BY created_at DESC LIMIT 1`, w.reviewID).Scan(&cfgRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return a.finishWork(ctx, id, "failed", "the review has no round yet", nil)
	}
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		return err
	}
	p, err := a.pr(ctx, w.reviewID)
	if err != nil {
		return err
	}
	f, err := a.findingByID(ctx, w.reviewID, w.findingID)
	if err != nil {
		return err
	}
	profile := cfg.Effective.Profiles[w.kind]
	if profile == "" {
		profile = w.kind
	}
	var targets []byte
	if err := a.Pool.QueryRow(ctx, `SELECT targets, round_budget_usd FROM profiles WHERE name = $1`, profile).Scan(&targets, &cfg.BudgetUSD); err != nil {
		return fmt.Errorf("profile %s: %w", profile, err)
	}
	cfg.Models = cfg.Models[:0]
	var ts []runner.Model
	_ = json.Unmarshal(targets, &ts)
	for _, t := range ts {
		t.Profile = profile
		cfg.Models = append(cfg.Models, t)
	}
	thread := []runner.Comment{}
	if f.CommentID != nil {
		cs, err := p.gh.Client.PullComments(ctx, p.gh.Token, p.gh.Inst, p.full, p.number)
		if err != nil {
			return err
		}
		for _, c := range cs {
			if c.ID == *f.CommentID || c.InReplyTo == *f.CommentID {
				thread = append(thread, runner.Comment{Author: c.User.Login, Body: c.Body})
			}
		}
	}
	entry := runner.LedgerEntry{ID: f.Label(), Severity: f.Severity, Category: f.Category, Title: f.Title, Path: f.Path, StartLine: f.StartLine, EndLine: f.EndLine, Status: f.Status}
	if n := len(thread); n > 0 && !strings.HasSuffix(thread[n-1].Author, "[bot]") {
		entry.LastHumanReply = thread[n-1].Body
	}
	spec := cfg.Spec
	// Execution off now wins over the round snapshot (SI-21).
	var execNow bool
	if err := a.Pool.QueryRow(ctx, `SELECT rp.execution FROM reviews rv JOIN repositories rp ON rp.id = rv.repository_id WHERE rv.id = $1`, w.reviewID).Scan(&execNow); err != nil {
		return err
	}
	spec.Execution = spec.Execution && execNow
	cfg.Effective.Execution = spec.Execution
	spec.Kind, spec.HeadSHA = w.kind, p.head
	spec.Stages = []string{w.kind}
	spec.Profiles = map[string]string{w.kind: profile}
	spec.Ledger = []runner.LedgerEntry{entry}
	spec.Reply, spec.Fix = nil, nil
	if w.kind == KindReply {
		spec.Reply = &runner.ReplyInput{Finding: entry, Thread: thread, Action: w.action}
	} else {
		spec.Fix = &runner.FixInput{Finding: entry, Thread: thread}
	}
	cfg.Spec = spec
	raw, _ := json.Marshal(cfg)
	_, err = a.Pool.Exec(ctx, `UPDATE agent_work SET spec = $2, status = 'running' WHERE id = $1`, id, raw)
	return err
}

func (a *Activities) findingByID(ctx context.Context, reviewID, id uuid.UUID) (*findings.Finding, error) {
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
		if fs[i].ID == id {
			return &fs[i], nil
		}
	}
	return nil, fmt.Errorf("finding %s not found", id)
}

// WorkStatus is the state of a work item after PrepareWork.
func (a *Activities) WorkStatus(ctx context.Context, id uuid.UUID) (string, error) {
	var s string
	err := a.Pool.QueryRow(ctx, `SELECT status FROM agent_work WHERE id = $1`, id).Scan(&s)
	return s, err
}

// ApplyWork posts the outcome of a reply or fix run (§11.3, §11.1 fix).
func (a *Activities) ApplyWork(ctx context.Context, id uuid.UUID) error {
	w, err := a.work(ctx, id)
	if err != nil {
		return err
	}
	var runID *uuid.UUID
	var runStatus, exit string
	if err := a.Pool.QueryRow(ctx, `SELECT w.agent_run_id, coalesce(r.status, ''), coalesce(r.exit_reason, '') FROM agent_work w LEFT JOIN agent_runs r ON r.id = w.agent_run_id WHERE w.id = $1`,
		id).Scan(&runID, &runStatus, &exit); err != nil {
		return err
	}
	if runID == nil || runStatus != "done" {
		return a.finishWork(ctx, id, "failed", "the agent run ended: "+exit, nil)
	}
	res, err := runner.ReadResult(ctx, a.Pool, a.Stores, *runID)
	if err != nil {
		return err
	}
	p, err := a.pr(ctx, w.reviewID)
	if err != nil {
		return err
	}
	f, err := a.findingByID(ctx, w.reviewID, w.findingID)
	if err != nil {
		return err
	}
	if w.kind == KindFix {
		return a.applyFix(ctx, w, p, f, res.Patch)
	}
	if res.Reply == nil {
		return a.finishWork(ctx, id, "failed", "the reply run gave no reply", nil)
	}
	c := comment{id: w.commentID, source: w.source}
	if w.source == "review" && f.CommentID != nil {
		c.inReplyTo = *f.CommentID
	}
	if err := a.answer(ctx, p, c, publish.Safe(res.Reply.Text)); err != nil {
		return err
	}
	var actor string
	_ = a.Pool.QueryRow(ctx, `SELECT actor_login FROM agent_work WHERE id = $1`, id).Scan(&actor)
	err = pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		switch res.Reply.Action {
		case "concede":
			if err := findings.SetStatus(ctx, tx, f.ID, nil, findings.StatusConceded, "conceded", "agent", "the reply stage conceded"); err != nil {
				return err
			}
			detail, _ := json.Marshal(map[string]string{"finding": f.Label()})
			_, err := tx.Exec(ctx, `INSERT INTO steering_signals (id, review_id, round_id, finding_id, type, actor_login, detail) VALUES ($1, $2, $3, $4, 'conceded', $5, $6)`,
				uuid.Must(uuid.NewV7()), w.reviewID, f.LastRoundID, f.ID, actor, detail)
			return err
		case "hold":
			return findings.Event(ctx, tx, f.ID, nil, "agent", "held", "the reply stage held the finding")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if res.Reply.Action == "concede" && f.ThreadNodeID != "" {
		if err := p.gh.Client.SetThreadResolved(ctx, p.gh.Token, p.gh.Inst, f.ThreadNodeID, true); err != nil {
			return err
		}
	}
	return a.finishWork(ctx, id, "done", "", res.Reply)
}

// applyFix opens a fix pull request from sayso/fix/<pr>-<n> into the head
// branch with the patch, and links it in the thread (§11.1).
func (a *Activities) applyFix(ctx context.Context, w work, p *prRef, f *findings.Finding, patch *runner.Patch) error {
	if patch == nil {
		return a.finishWork(ctx, w.id, "failed", "the fix run gave no patch", nil)
	}
	files, _, err := gitdiff.Parse(strings.NewReader(patch.Diff))
	if err != nil {
		return a.finishWork(ctx, w.id, "failed", "the patch does not parse: "+err.Error(), nil)
	}
	pr, err := p.gh.Client.GetPull(ctx, p.gh.Token, p.gh.Inst, p.full, p.number)
	if err != nil {
		return err
	}
	// Check every path before Sayso writes anything.
	paths := make([]string, len(files))
	for i, fd := range files {
		if fd.IsDelete || fd.IsBinary || fd.IsRename || fd.IsCopy {
			return a.finishWork(ctx, w.id, "failed", "the patch deletes, renames, copies or changes a binary file; Sayso commits only text changes", nil)
		}
		path, ok := patchPath(fd.NewName)
		if !ok {
			return a.finishWork(ctx, w.id, "failed", "the patch has a file path outside the repository", nil)
		}
		paths[i] = path
	}
	branch := fmt.Sprintf("sayso/fix/%d-%d", p.number, f.Seq)
	// Sayso commits only to a branch it created (SI-07). A retry reuses the
	// branch when this work recorded that it created it.
	if err := p.gh.Client.CreateBranch(ctx, p.gh.Token, p.gh.Inst, p.full, branch, pr.Head.SHA); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return err
		}
		var created bool
		if err := a.Pool.QueryRow(ctx, `SELECT branch_created FROM agent_work WHERE id = $1`, w.id).Scan(&created); err != nil {
			return err
		}
		if !created {
			return a.finishWork(ctx, w.id, "failed", "the branch "+branch+" exists and Sayso did not create it", nil)
		}
	} else if _, err := a.Pool.Exec(ctx, `UPDATE agent_work SET branch_created = true WHERE id = $1`, w.id); err != nil {
		return err
	}
	for i, fd := range files {
		path := paths[i]
		old, sha, _, err := p.gh.Client.FileWithSHA(ctx, p.gh.Token, p.gh.Inst, p.full, path, branch)
		if err != nil {
			return err
		}
		var out bytes.Buffer
		if err := gitdiff.Apply(&out, bytes.NewReader(old), fd); err != nil {
			// The branch has the patch already when an earlier attempt committed it.
			if strings.Contains(err.Error(), "conflict") || strings.Contains(err.Error(), "fragment") {
				continue
			}
			return a.finishWork(ctx, w.id, "failed", "the patch does not apply to "+path+": "+err.Error(), nil)
		}
		msg := fmt.Sprintf("Fix %s: %s", f.Label(), publish.Safe(patch.Title))
		if err := p.gh.Client.PutFile(ctx, p.gh.Token, p.gh.Inst, p.full, branch, path, msg, out.Bytes(), sha); err != nil {
			return err
		}
	}
	body := publish.Safe(patch.Description) + fmt.Sprintf("\n\nThis pull request fixes %s of #%d. Sayso does not review it.", f.Label(), p.number)
	fix, err := p.gh.Client.CreatePull(ctx, p.gh.Token, p.gh.Inst, p.full, "Fix "+f.Label()+": "+publish.Safe(patch.Title), body, branch, pr.Head.Ref)
	if err != nil {
		return err
	}
	c := comment{id: w.commentID, source: w.source}
	if f.CommentID != nil {
		c = comment{id: *f.CommentID, source: "review"}
	}
	if err := a.answer(ctx, p, c, fmt.Sprintf("Sayso opened #%d with a fix for %s.", fix.Number, f.Label())); err != nil {
		return err
	}
	return a.finishWork(ctx, w.id, "done", "", map[string]any{"pull": fix.Number, "branch": branch})
}

// FailWork records a work item that did not finish.
func (a *Activities) FailWork(ctx context.Context, id uuid.UUID, reason string) error {
	return a.finishWork(ctx, id, "failed", reason, nil)
}

// patchPath strips the a/ or b/ prefix like git apply -p1, which the runtime
// checks the patch with, and accepts only a plain path inside the repository.
func patchPath(name string) (string, bool) {
	for _, pre := range []string{"b/", "a/"} {
		if p, ok := strings.CutPrefix(name, pre); ok {
			name = p
			break
		}
	}
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") {
		return "", false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." || seg == ".git" {
			return "", false
		}
	}
	return name, true
}
