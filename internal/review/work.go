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
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/publish"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/settings"
)

// WorkInput names a queued reply or fix run.
type WorkInput struct {
	WorkID       uuid.UUID `json:"work_id"`
	RepositoryID uuid.UUID `json:"repository_id"`
	Kind         string    `json:"kind"`
}

type work struct {
	id, reviewID, findingID uuid.UUID
	// findingIDs are the findings of a fix, findingID first.
	findingIDs           []uuid.UUID
	kind, action, source string
	commentID            int64
	head                 string
}

func (a *Activities) work(ctx context.Context, id uuid.UUID) (work, error) {
	w := work{id: id}
	err := a.Pool.QueryRow(ctx, `SELECT review_id, finding_id, finding_ids, kind, action, source, comment_id, head_sha FROM agent_work WHERE id = $1`, id).
		Scan(&w.reviewID, &w.findingID, &w.findingIDs, &w.kind, &w.action, &w.source, &w.commentID, &w.head)
	if len(w.findingIDs) == 0 {
		w.findingIDs = []uuid.UUID{w.findingID}
	}
	return w, err
}

// workTimeout is the time limit of a work item in minutes. A fix run gets
// the round timeout for each finding.
func (a *Activities) workTimeout(ctx context.Context, w work) int {
	if w.kind == KindFix {
		return a.intSetting(ctx, settings.RoundTimeoutMinutes, 30) * len(w.findingIDs)
	}
	return a.intSetting(ctx, settings.ReplyTimeoutMinutes, 10)
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
	// A fix run also has the fix_check stage, on the challenge profile.
	stages := []string{w.kind}
	profiles := map[string]string{}
	if w.kind == KindFix {
		stages = append(stages, runner.StageFixCheck)
	}
	cfg.Models = cfg.Models[:0]
	for _, stage := range stages {
		key := stage
		if stage == runner.StageFixCheck {
			key = runner.StageChallenge
		}
		profile := cfg.Effective.Profiles[key]
		if profile == "" {
			profile = key
		}
		profiles[stage] = profile
		var targets []byte
		var budget float64
		if err := a.Pool.QueryRow(ctx, `SELECT targets, round_budget_usd FROM profiles WHERE name = $1`, profile).Scan(&targets, &budget); err != nil {
			return fmt.Errorf("profile %s: %w", profile, err)
		}
		var ts []runner.Model
		_ = json.Unmarshal(targets, &ts)
		if len(ts) == 0 {
			return a.finishWork(ctx, id, "failed", NoModelTarget(stage, profile), nil)
		}
		if stage == w.kind {
			cfg.BudgetUSD = budget
		}
		for _, t := range ts {
			t.Profile = profile
			cfg.Models = append(cfg.Models, t)
		}
	}
	var comments []github.PullComment
	var targets []runner.FixTarget
	var entries []runner.LedgerEntry
	for _, fid := range w.findingIDs {
		f, err := a.findingByID(ctx, w.reviewID, fid)
		if err != nil {
			return err
		}
		thread := []runner.Comment{}
		if f.CommentID != nil {
			if comments == nil {
				if comments, err = p.gh.Client.PullComments(ctx, p.gh.Token, p.gh.Inst, p.full, p.number); err != nil {
					return err
				}
			}
			for _, c := range comments {
				if c.ID == *f.CommentID || c.InReplyTo == *f.CommentID {
					thread = append(thread, runner.Comment{Author: c.User.Login, Body: c.Body})
				}
			}
		}
		entry := runner.LedgerEntry{ID: f.Label(), Severity: f.Severity, Category: f.Category, Title: f.Title, Path: f.Path, StartLine: f.StartLine, EndLine: f.EndLine, Status: f.Status}
		if n := len(thread); n > 0 && !strings.HasSuffix(thread[n-1].Author, "[bot]") {
			entry.LastHumanReply = thread[n-1].Body
		}
		entries = append(entries, entry)
		targets = append(targets, runner.FixTarget{Finding: entry, Thread: thread})
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
	spec.Stages = stages
	spec.Profiles = profiles
	spec.Ledger = entries
	spec.Reply, spec.Fix = nil, nil
	if w.kind == KindReply {
		spec.Reply = &runner.ReplyInput{Finding: entries[0], Thread: targets[0].Thread, Action: w.action}
	} else {
		// The budgets scale with the number of findings.
		n := len(targets)
		spec.Limits.TimeoutSeconds = a.workTimeout(ctx, w) * 60
		spec.Limits.MaxToolCallsPerStage = a.intSetting(ctx, settings.MaxToolCallsPerStage, 200) * n
		cfg.BudgetUSD *= float64(n)
		spec.Fix = &runner.FixInput{Findings: targets}
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
	if w.kind == KindFix {
		return a.applyFix(ctx, w, p, res)
	}
	f, err := a.findingByID(ctx, w.reviewID, w.findingID)
	if err != nil {
		return err
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

// fixOutcome is agent_work.outcome of a fix: the fix pull request and the
// result of each finding. Commits counts the patches on the branch, so a
// retry goes on after the last one.
type fixOutcome struct {
	Pull    int                `json:"pull,omitempty"`
	Branch  string             `json:"branch"`
	Commits int                `json:"commits"`
	Results []runner.FixResult `json:"results"`
}

// fixBranchName is sayso/fix/<pr>-<n> for one finding, and
// sayso/fix/<pr>-batch-<round>-<work> for several.
func (a *Activities) fixBranchName(ctx context.Context, w work, p *prRef, fs []*findings.Finding) (string, error) {
	if len(fs) == 1 {
		return fmt.Sprintf("sayso/fix/%d-%d", p.number, fs[0].Seq), nil
	}
	var rounds int
	if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM rounds WHERE review_id = $1`, w.reviewID).Scan(&rounds); err != nil {
		return "", err
	}
	return fmt.Sprintf("sayso/fix/%d-batch-%d-%s", p.number, rounds, w.id.String()[:8]), nil
}

// applyFix opens one fix pull request into the head branch with one commit
// for each fix that passed fix_check, and answers the command with the
// result of each finding (§11.1, the batch fix spec). With no fix left it
// opens nothing.
func (a *Activities) applyFix(ctx context.Context, w work, p *prRef, res *runner.Result) error {
	fs := make([]*findings.Finding, 0, len(w.findingIDs))
	byLabel := map[string]*findings.Finding{}
	for _, id := range w.findingIDs {
		f, err := a.findingByID(ctx, w.reviewID, id)
		if err != nil {
			return err
		}
		fs = append(fs, f)
		byLabel[f.Label()] = f
	}
	results := map[string]runner.FixResult{}
	for _, r := range res.FixResults {
		if byLabel[r.FindingID] != nil {
			results[r.FindingID] = r
		}
	}
	setResult := func(label, status, reason string) {
		results[label] = runner.FixResult{FindingID: label, Status: status, Reason: reason}
	}
	// Check every patch before Sayso writes anything.
	type parsed struct {
		patch runner.Patch
		f     *findings.Finding
		files []*gitdiff.File
		paths []string
	}
	var patches []parsed
	for _, pt := range res.Patches {
		f := byLabel[pt.FindingID]
		if f == nil {
			continue
		}
		files, _, err := gitdiff.Parse(strings.NewReader(pt.Diff))
		if err != nil {
			setResult(f.Label(), "dropped", "the patch does not parse: "+err.Error())
			continue
		}
		paths, why := patchPaths(files)
		if why != "" {
			setResult(f.Label(), "dropped", why)
			continue
		}
		patches = append(patches, parsed{patch: pt, f: f, files: files, paths: paths})
	}
	ordered := func() []runner.FixResult {
		out := make([]runner.FixResult, 0, len(fs))
		for _, f := range fs {
			r, ok := results[f.Label()]
			if !ok {
				r = runner.FixResult{FindingID: f.Label(), Status: "skipped", Reason: "the fix run gave no result for it"}
			}
			out = append(out, r)
		}
		return out
	}
	reply := comment{id: w.commentID, source: w.source}
	if len(fs) == 1 && fs[0].CommentID != nil {
		reply = comment{id: *fs[0].CommentID, source: "review"}
	}
	if len(patches) == 0 {
		out := fixOutcome{Results: ordered()}
		if err := a.answer(ctx, p, reply, "Sayso opened no fix pull request. No fix passed the check.\n\n"+fixList(out.Results)); err != nil {
			return err
		}
		return a.finishWork(ctx, w.id, "failed", "no fix passed the check", out)
	}

	pr, err := p.gh.Client.GetPull(ctx, p.gh.Token, p.gh.Inst, p.full, p.number)
	if err != nil {
		return err
	}
	branch, err := a.fixBranchName(ctx, w, p, fs)
	if err != nil {
		return err
	}
	var prior fixOutcome
	var raw []byte
	if err := a.Pool.QueryRow(ctx, `SELECT outcome FROM agent_work WHERE id = $1`, w.id).Scan(&raw); err != nil {
		return err
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &prior)
	}
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
	committed := prior.Commits
	if prior.Branch != branch {
		committed = 0
	}
	kept := 0
	for i, pt := range patches {
		if i < committed {
			kept++
			setResult(pt.f.Label(), "fixed", "")
			continue
		}
		changed := map[string][]byte{}
		var applyErr error
		for j, fd := range pt.files {
			old, _, _, err := p.gh.Client.FileWithSHA(ctx, p.gh.Token, p.gh.Inst, p.full, pt.paths[j], branch)
			if err != nil {
				return err
			}
			var out bytes.Buffer
			if err := gitdiff.Apply(&out, bytes.NewReader(old), fd); err != nil {
				applyErr = fmt.Errorf("%s: %w", pt.paths[j], err)
				break
			}
			changed[pt.paths[j]] = out.Bytes()
		}
		if applyErr != nil {
			// The head moved, or a dropped fix came before it.
			setResult(pt.f.Label(), "dropped", "the patch does not apply to the head branch: "+applyErr.Error())
		} else {
			msg := fmt.Sprintf("Fix %s: %s", pt.f.Label(), publish.Safe(pt.patch.Title))
			if _, err := p.gh.Client.CommitFiles(ctx, p.gh.Token, p.gh.Inst, p.full, branch, msg, changed); err != nil {
				return err
			}
			kept++
			setResult(pt.f.Label(), "fixed", "")
		}
		// Record progress, so a retry does not commit a patch twice.
		progress, _ := json.Marshal(fixOutcome{Branch: branch, Commits: i + 1})
		if _, err := a.Pool.Exec(ctx, `UPDATE agent_work SET outcome = $2 WHERE id = $1`, w.id, progress); err != nil {
			return err
		}
	}
	out := fixOutcome{Branch: branch, Commits: len(patches), Results: ordered()}
	if kept == 0 {
		if err := a.answer(ctx, p, reply, "Sayso opened no fix pull request. No fix applies to the head branch.\n\n"+fixList(out.Results)); err != nil {
			return err
		}
		return a.finishWork(ctx, w.id, "failed", "no fix applies to the head branch", out)
	}
	labels := make([]string, 0, len(fs))
	for _, f := range fs {
		labels = append(labels, f.Label())
	}
	title := "Fix " + strings.Join(labels, ", ")
	if len(patches) == 1 {
		title += ": " + publish.Safe(patches[0].patch.Title)
	}
	var body strings.Builder
	for _, pt := range patches {
		if results[pt.f.Label()].Status == "fixed" {
			fmt.Fprintf(&body, "### %s: %s\n\n%s\n\n", pt.f.Label(), publish.Safe(pt.patch.Title), publish.Safe(pt.patch.Description))
		}
	}
	fmt.Fprintf(&body, "## Results\n\n%s\n\nThis pull request fixes findings of #%d, with one commit for each fix. A check stage confirmed each fix against the patched code. Sayso does not review this pull request. When you merge it, Sayso starts a new round on #%d.",
		fixList(out.Results), p.number, p.number)
	fix, err := p.gh.Client.CreatePull(ctx, p.gh.Token, p.gh.Inst, p.full, title, body.String(), branch, pr.Head.Ref)
	if err != nil {
		return err
	}
	out.Pull = fix.Number
	if err := a.answer(ctx, p, reply, fmt.Sprintf("Sayso opened #%d.\n\n%s", fix.Number, fixList(out.Results))); err != nil {
		return err
	}
	return a.finishWork(ctx, w.id, "done", "", out)
}

// fixList is the result of each finding as a Markdown list.
func fixList(rs []runner.FixResult) string {
	var b strings.Builder
	for _, r := range rs {
		switch r.Status {
		case "fixed":
			fmt.Fprintf(&b, "- %s: fixed and checked\n", r.FindingID)
		case "skipped":
			fmt.Fprintf(&b, "- %s: skipped: %s\n", r.FindingID, publish.Safe(r.Reason))
		default:
			fmt.Fprintf(&b, "- %s: dropped by the check: %s\n", r.FindingID, publish.Safe(r.Reason))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// patchPaths checks the files of a patch: Sayso commits only text changes
// inside the repository.
func patchPaths(files []*gitdiff.File) ([]string, string) {
	paths := make([]string, len(files))
	for i, fd := range files {
		if fd.IsDelete || fd.IsBinary || fd.IsRename || fd.IsCopy {
			return nil, "the patch deletes, renames, copies or changes a binary file; Sayso commits only text changes"
		}
		path, ok := patchPath(fd.NewName)
		if !ok {
			return nil, "the patch has a file path outside the repository"
		}
		paths[i] = path
	}
	return paths, ""
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
