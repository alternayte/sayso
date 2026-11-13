package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/feeds"
	"github.com/alternayte/sayso/internal/findings"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/publish"
	"github.com/alternayte/sayso/internal/repos"
	"github.com/alternayte/sayso/internal/review/signals"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/settings"
	"github.com/alternayte/sayso/internal/skills"
	"github.com/alternayte/sayso/internal/storage"
)

// SkipLabel stops new rounds on a pull request (§7.1).
const SkipLabel = "sayso:skip"

// Activities are the short activities of the reviews task queue.
type Activities struct {
	Pool       *pgxpool.Pool
	Settings   *settings.Store
	GitHub     func(ctx context.Context, inst int64, fullName string) (publish.Session, error)
	Publisher  *publish.Publisher
	Skills     *skills.Resolver
	Extensions *skills.Extensions
	Feeds      *feeds.Service
	Reporter   *skills.Reporter
	Stores     *storage.Registry
	// AppLogin is the login of the App bot, for REQ-LIFE-011.
	AppLogin func(ctx context.Context) string
}

func (a *Activities) intSetting(ctx context.Context, key string, def int) int {
	v := def
	if err := a.Settings.Decode(ctx, key, &v); err != nil || v <= 0 {
		return def
	}
	return v
}

// EnsureReview creates the review row of a pull request on the first signal.
func (a *Activities) EnsureReview(ctx context.Context, s signals.Start) (uuid.UUID, error) {
	id := uuid.Must(uuid.NewV7())
	err := a.Pool.QueryRow(ctx, `INSERT INTO reviews (id, repository_id, pr_number, workflow_id) VALUES ($1, $2, $3, $4)
		ON CONFLICT (repository_id, pr_number) DO UPDATE SET workflow_id = EXCLUDED.workflow_id RETURNING id`,
		id, s.RepositoryID, s.Number, signals.WorkflowID(s.RepoGitHubID, s.Number)).Scan(&id)
	return id, err
}

// Decision tells the workflow whether a head gets a round.
type Decision struct {
	RoundID *uuid.UUID `json:"round_id,omitempty"`
	Reason  string     `json:"reason,omitempty"`
	Paused  bool       `json:"paused,omitempty"`
}

// DecideInput holds the IDs of a round candidate.
type DecideInput struct {
	ReviewID uuid.UUID `json:"review_id"`
	HeadSHA  string    `json:"head_sha"`
	BaseSHA  string    `json:"base_sha"`
	Trigger  string    `json:"trigger"`
	// Paused is the pause state of the workflow (command pause).
	Paused bool `json:"paused"`
}

// Decide checks the rules of §7.1 and §7.3 and, when a round may start,
// creates it queued. It reads the pull request from GitHub, so the rules
// see the current draft state and labels.
func (a *Activities) Decide(ctx context.Context, in DecideInput) (Decision, error) {
	var repoID uuid.UUID
	var full, mode string
	var inst int64
	var number int
	err := a.Pool.QueryRow(ctx, `SELECT rp.id, rp.full_name, rp.mode, rp.installation_id, rv.pr_number FROM reviews rv JOIN repositories rp ON rp.id = rv.repository_id
		WHERE rv.id = $1`, in.ReviewID).Scan(&repoID, &full, &mode, &inst, &number)
	if err != nil {
		return Decision{}, err
	}
	if mode == "off" {
		return Decision{Reason: "repository mode is off"}, nil
	}
	gh, err := a.GitHub(ctx, inst, full)
	if err != nil {
		return Decision{}, err
	}
	pr, err := gh.Client.GetPull(ctx, gh.Token, gh.Inst, full, number)
	if err != nil {
		return Decision{}, err
	}
	head, base := in.HeadSHA, in.BaseSHA
	if head == "" || pr.Head.SHA != head {
		head = pr.Head.SHA
	}
	if base == "" {
		base = pr.Base.SHA
	}
	if _, err := a.Pool.Exec(ctx, `UPDATE reviews SET head_sha = $2, base_sha = $3, pr_node_id = $4, author_login = $5 WHERE id = $1`,
		in.ReviewID, head, base, pr.NodeID, pr.User.Login); err != nil {
		return Decision{}, err
	}
	explicit := in.Trigger == signals.TriggerCommand || in.Trigger == signals.TriggerCheckRerequest
	switch {
	case pr.State != "open":
		return Decision{Reason: "the pull request is closed"}, nil
	case a.AppLogin != nil && strings.EqualFold(pr.User.Login, a.AppLogin(ctx)):
		return Decision{Reason: "Sayso opened this pull request"}, nil
	case pr.Draft:
		return Decision{Reason: "the pull request is a draft", Paused: true}, nil
	case slices.ContainsFunc(pr.Labels, func(l struct {
		Name string `json:"name"`
	}) bool {
		return l.Name == SkipLabel
	}):
		return Decision{Reason: "the pull request has the " + SkipLabel + " label", Paused: true}, nil
	case in.Paused && !explicit:
		return Decision{Reason: "the review is paused", Paused: true}, nil
	}
	if !explicit {
		var n int
		if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM rounds WHERE review_id = $1 AND head_sha = $2 AND status IN ('queued', 'running', 'publishing', 'done')`,
			in.ReviewID, head).Scan(&n); err != nil {
			return Decision{}, err
		}
		if n > 0 {
			return Decision{Reason: "this head has a round"}, nil
		}
	}
	if spent, err := a.MonthlyBudgetSpent(ctx); err != nil {
		return Decision{}, err
	} else if spent {
		return Decision{Reason: "the monthly budget is spent"}, nil
	}
	id := uuid.Must(uuid.NewV7())
	trigger := in.Trigger
	if _, err := a.Pool.Exec(ctx, `INSERT INTO rounds (id, review_id, head_sha, base_sha, trigger) VALUES ($1, $2, $3, $4, $5)`, id, in.ReviewID, head, base, trigger); err != nil {
		return Decision{}, err
	}
	return Decision{RoundID: &id}, nil
}

// MonthlyBudgetSpent compares the model cost of this month with the
// monthly budget setting (§7.3). Zero means no budget.
func (a *Activities) MonthlyBudgetSpent(ctx context.Context) (bool, error) {
	spent, budget, err := MonthlySpend(ctx, a.Pool, a.Settings)
	return budget > 0 && spent >= budget, err
}

// SetReviewState sets open, paused, closed or merged.
func (a *Activities) SetReviewState(ctx context.Context, reviewID uuid.UUID, state string) error {
	_, err := a.Pool.Exec(ctx, `UPDATE reviews SET state = $2, closed_at = CASE WHEN $2 IN ('closed', 'merged') THEN now() END WHERE id = $1`, reviewID, state)
	return err
}

// Config is what ResolveConfig stores in rounds.config_snapshot.
type Config struct {
	Effective repos.Effective   `json:"effective"`
	Overlay   runner.Overlay    `json:"overlay"`
	Profiles  map[string]string `json:"profiles"`
	Image     string            `json:"image,omitempty"`
	BudgetUSD float64           `json:"budget_usd"`
	Models    []runner.Model    `json:"models"`
	Spec      runner.Spec       `json:"spec"`
}

// ResolveConfig reads the settings, the repository and .sayso.yml of the
// base commit, resolves the skill, profiles, MCP servers, extensions,
// feeds and image, and stores the snapshot (§7.2 step 1).
func (a *Activities) ResolveConfig(ctx context.Context, roundID uuid.UUID) error {
	var reviewID uuid.UUID
	var head, base string
	var number int
	var repo repos.Repository
	var fullName string
	err := a.Pool.QueryRow(ctx, `SELECT ro.review_id, ro.head_sha, ro.base_sha, rv.pr_number, rp.full_name FROM rounds ro JOIN reviews rv ON rv.id = ro.review_id
		JOIN repositories rp ON rp.id = rv.repository_id WHERE ro.id = $1`, roundID).Scan(&reviewID, &head, &base, &number, &fullName)
	if err != nil {
		return err
	}
	if repo, err = repos.New(a.Pool).ByFullName(ctx, fullName); err != nil {
		return err
	}
	gh, err := a.GitHub(ctx, repo.InstallationID, repo.FullName)
	if err != nil {
		return err
	}
	read := func(ctx context.Context, path, ref string) ([]byte, bool, error) {
		b, err := gh.Client.FileAt(ctx, gh.Token, gh.Inst, repo.FullName, path, ref)
		if errors.Is(err, github.ErrNotFound) {
			return nil, false, nil
		}
		return b, err == nil, err
	}
	var ov repos.Overlay
	var warnings []string
	if b, found, err := read(ctx, ".sayso.yml", base); err != nil {
		return err
	} else if found {
		if ov, warnings, err = repos.ParseOverlay(b); err != nil {
			warnings = append(warnings, ".sayso.yml: "+err.Error())
		}
	}
	var allowed []string
	_ = a.Settings.Decode(ctx, settings.AllowedProfiles, &allowed)
	enabled, err := a.Extensions.Enabled(ctx, repo.FullName)
	if err != nil {
		return err
	}
	var images []string
	rows, err := a.Pool.Query(ctx, `SELECT name FROM agent_images ORDER BY name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		images = append(images, n)
	}
	rows.Close()
	eff := repos.Resolve(repo, ov, warnings, repos.Limits{AllowedProfiles: allowed, MaxInlineComments: a.intSetting(ctx, settings.MaxInlineComments, 10),
		Extensions: enabled, Images: images})

	skillPath := eff.Skill
	if skillPath == "" {
		skillPath = repos.DefaultSkillPath
	}
	skill, skael, err := a.Skills.Resolve(ctx, read, skillPath, base, repo.SkaelSet)
	if err != nil {
		return err
	}
	pr, err := gh.Client.GetPull(ctx, gh.Token, gh.Inst, repo.FullName, number)
	if err != nil {
		return err
	}
	files, err := gh.Client.PullFiles(ctx, gh.Token, gh.Inst, repo.FullName, number)
	if err != nil {
		return err
	}
	changed := make([]string, 0, len(files))
	adds, dels := 0, 0
	for _, f := range files {
		changed = append(changed, f.Filename)
		for _, l := range strings.Split(f.Patch, "\n") {
			switch {
			case strings.HasPrefix(l, "+"):
				adds++
			case strings.HasPrefix(l, "-"):
				dels++
			}
		}
	}
	labels := []string{}
	for _, l := range pr.Labels {
		labels = append(labels, l.Name)
	}

	profiles := map[string]string{"review": eff.Profiles["review"], "challenge": eff.Profiles["challenge"]}
	var models []runner.Model
	budget := 0.0
	for _, stage := range []string{"review", "challenge"} {
		var targets []byte
		var b float64
		if err := a.Pool.QueryRow(ctx, `SELECT targets, round_budget_usd FROM profiles WHERE name = $1`, profiles[stage]).Scan(&targets, &b); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("profile %q of stage %s does not exist", profiles[stage], stage)
			}
			return err
		}
		if stage == "review" {
			budget = b
		}
		var ts []struct {
			Provider       string   `json:"provider"`
			Model          string   `json:"model"`
			Temperature    *float64 `json:"temperature"`
			MaxTokens      int      `json:"max_tokens"`
			Reasoning      string   `json:"reasoning"`
			ContextWindow  int      `json:"context_window"`
			PriceIn        float64  `json:"price_in"`
			PriceOut       float64  `json:"price_out"`
			PriceCacheRead float64  `json:"price_cache_read"`
		}
		_ = json.Unmarshal(targets, &ts)
		for _, t := range ts {
			models = append(models, runner.Model{Profile: profiles[stage], Provider: t.Provider, Model: t.Model, MaxTokens: t.MaxTokens, ContextWindow: t.ContextWindow,
				Reasoning: t.Reasoning, Temperature: t.Temperature, PriceIn: t.PriceIn, PriceOut: t.PriceOut, PriceCacheRead: t.PriceCacheRead})
		}
	}

	ext, err := a.Extensions.Spec(ctx, eff.Extensions)
	if err != nil {
		return err
	}
	sf, err := a.Feeds.SpecFeeds(ctx, repo.FullName, eff.Feeds)
	if err != nil {
		return err
	}
	if sf == nil {
		sf = []runner.SpecFeed{}
	}
	mcp := []runner.SpecMCP{}
	for _, m := range eff.MCP {
		mcp = append(mcp, runner.SpecMCP{Name: m})
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	ledger, err := findings.List(ctx, tx, reviewID)
	_ = tx.Rollback(ctx)
	if err != nil {
		return err
	}
	entries := []runner.LedgerEntry{}
	for _, f := range ledger {
		if f.Status == findings.StatusOpen {
			entries = append(entries, runner.LedgerEntry{ID: f.Label(), Severity: f.Severity, Category: f.Category, Title: f.Title, Path: f.Path,
				StartLine: f.StartLine, EndLine: f.EndLine, Status: f.Status})
		}
	}
	overlay := runner.Overlay{Instructions: eff.Instructions, Ignore: eff.Ignore, Nits: eff.Nits, Block: eff.Block, Toolchain: eff.Toolchain, Restore: eff.Restore,
		Setup: eff.Setup, MaxInlineComments: eff.MaxInlineComments}
	if overlay.Toolchain == "" {
		overlay.Toolchain = "auto"
	}
	if overlay.Restore == "" {
		overlay.Restore = "auto"
	}
	image := ""
	if eff.Image != "" {
		_ = a.Pool.QueryRow(ctx, `SELECT image FROM agent_images WHERE name = $1`, eff.Image).Scan(&image)
	} else {
		_ = a.Pool.QueryRow(ctx, `SELECT image FROM agent_images WHERE is_default`).Scan(&image)
	}
	timeout := a.intSetting(ctx, settings.RoundTimeoutMinutes, 30)
	spec := runner.Spec{
		Kind:        "round",
		Stages:      []string{"review", "challenge"},
		Repository:  runner.SpecRepository{FullName: repo.FullName, DefaultBranch: repo.DefaultBranch},
		BaseSHA:     base,
		HeadSHA:     head,
		PullRequest: runner.SpecPullRequest{Number: number, Title: pr.Title, Body: pr.Body, Labels: labels, Author: pr.User.Login, DiffStat: fmt.Sprintf("%d files, +%d -%d", len(files), adds, dels), ChangedFiles: changed},
		Ledger:      entries,
		Overlay:     overlay,
		Skill:       skill,
		Skael:       skael,
		Profiles:    profiles,
		MCP:         mcp,
		Extensions:  ext,
		Feeds:       sf,
		Limits: runner.Limits{MaxFindings: a.intSetting(ctx, settings.MaxFindingsPerRound, 25), MaxToolCallsPerStage: a.intSetting(ctx, settings.MaxToolCallsPerStage, 200),
			TimeoutSeconds: timeout * 60, SetupTimeoutSeconds: a.intSetting(ctx, settings.SetupTimeoutMinutes, 10) * 60, ChallengeMinSeverity: eff.ChallengeMin},
		Prompts:   runner.Prompts{File: runner.PromptLayer{Version: base, Text: eff.Instructions}},
		Execution: eff.Execution,
	}
	// Extension secrets stay out of the stored snapshot; RunAgent reads them again.
	stored := spec
	stored.Extensions = []runner.SpecExtension{}
	for _, e := range ext {
		stored.Extensions = append(stored.Extensions, runner.SpecExtension{Name: e.Name, Package: e.Package})
	}
	stored.Feeds = []runner.SpecFeed{}
	for _, f := range sf {
		f.Credential = ""
		stored.Feeds = append(stored.Feeds, f)
	}
	cfg := Config{Effective: eff, Overlay: overlay, Profiles: profiles, Image: image, BudgetUSD: budget, Models: models, Spec: stored}
	raw, _ := json.Marshal(cfg)
	_, err = a.Pool.Exec(ctx, `UPDATE rounds SET config_snapshot = $2 WHERE id = $1`, roundID, raw)
	return err
}

// MarkStarted sets the round running and shows it on GitHub (§7.2 step 2).
func (a *Activities) MarkStarted(ctx context.Context, roundID uuid.UUID) error {
	if _, err := a.Pool.Exec(ctx, `UPDATE rounds SET status = 'running', started_at = coalesce(started_at, now()) WHERE id = $1 AND status = 'queued'`, roundID); err != nil {
		return err
	}
	return a.Publisher.MarkStarted(ctx, roundID)
}

// Finish ends a round that did not reach Publish: failed, budget_exceeded
// or cancelled. It sets the neutral check and the status line.
type Finish struct {
	RoundID uuid.UUID `json:"round_id"`
	Status  string    `json:"status"`
	Reason  string    `json:"reason"`
}

// FinishRound records the end of a round and publishes its state.
func (a *Activities) FinishRound(ctx context.Context, f Finish) error {
	// A cancel reason that the PRReview workflow stored wins; a cancel
	// without one came from the Temporal CLI.
	if _, err := a.Pool.Exec(ctx, `UPDATE rounds SET status = $2, failure_reason = CASE WHEN $2 = 'cancelled' AND cancelled_by <> '' THEN cancelled_by ELSE $3 END,
		finished_at = now() WHERE id = $1 AND status NOT IN ('done', 'failed', 'cancelled', 'budget_exceeded')`, f.RoundID, f.Status, f.Reason); err != nil {
		return err
	}
	if _, err := a.Pool.Exec(ctx, `UPDATE agent_runs SET cancel_requested = true WHERE id = (SELECT agent_run_id FROM rounds WHERE id = $1) AND status IN ('queued', 'running')`, f.RoundID); err != nil {
		return err
	}
	_, err := a.Publisher.Publish(ctx, f.RoundID)
	return err
}

// Reconciled is the result of ReconcileLedger.
type Reconciled struct {
	// Status is failed or budget_exceeded when the agent run did not finish.
	Status string `json:"status,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ReconcileLedger applies result.json of the round's agent run to the
// ledger (§7.2 step 4).
func (a *Activities) ReconcileLedger(ctx context.Context, roundID uuid.UUID) (Reconciled, error) {
	var runID *uuid.UUID
	var cfgRaw []byte
	var reviewID, repoID uuid.UUID
	var head string
	err := a.Pool.QueryRow(ctx, `SELECT ro.agent_run_id, ro.config_snapshot, ro.review_id, rv.repository_id, ro.head_sha FROM rounds ro JOIN reviews rv ON rv.id = ro.review_id
		WHERE ro.id = $1`, roundID).Scan(&runID, &cfgRaw, &reviewID, &repoID, &head)
	if err != nil {
		return Reconciled{}, err
	}
	if runID == nil {
		return Reconciled{Status: "failed", Reason: "the round has no agent run"}, nil
	}
	var status, exit string
	if err := a.Pool.QueryRow(ctx, `SELECT status, exit_reason FROM agent_runs WHERE id = $1`, *runID).Scan(&status, &exit); err != nil {
		return Reconciled{}, err
	}
	// The model proxy refuses a request over the round budget; the run then
	// fails, and its events show why (§7.3).
	var refused bool
	if err := a.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM run_events WHERE run_id = $1 AND type = 'error' AND name = 'budget_exceeded')`, *runID).Scan(&refused); err != nil {
		return Reconciled{}, err
	}
	if refused {
		status = "budget_exceeded"
	}
	switch status {
	case "budget_exceeded":
		return Reconciled{Status: "budget_exceeded", Reason: "the round budget is spent"}, nil
	case "done":
	default:
		reason := exit
		if reason == "" {
			reason = "the agent run ended with status " + status
		}
		return Reconciled{Status: "failed", Reason: reason}, nil
	}
	res, err := runner.ReadResult(ctx, a.Pool, a.Stores, *runID)
	if err != nil {
		return Reconciled{}, err
	}
	var cfg Config
	_ = json.Unmarshal(cfgRaw, &cfg)
	var threshold float64
	_ = a.Settings.Decode(ctx, settings.MatchSimilarity, &threshold)
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return Reconciled{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A retry after a crash must not apply the result twice.
	var applied []byte
	if err := tx.QueryRow(ctx, `SELECT ledger_outcome FROM rounds WHERE id = $1 FOR UPDATE`, roundID).Scan(&applied); err != nil {
		return Reconciled{}, err
	}
	if len(applied) > 0 {
		return Reconciled{}, nil
	}
	out, err := findings.Reconcile(ctx, tx, findings.Input{ReviewID: reviewID, RepositoryID: repoID, RoundID: roundID, HeadSHA: head, Result: *res,
		Block: cfg.Overlay.Block, Threshold: threshold})
	if err != nil {
		return Reconciled{}, err
	}
	outRaw, _ := json.Marshal(out)
	var assessment, skillRef, promptRef []byte
	risk := (*string)(nil)
	if res.Assessment != nil {
		assessment, _ = json.Marshal(res.Assessment)
		risk = &res.Assessment.Risk
	}
	if res.SkillRef != nil {
		skillRef, _ = json.Marshal(res.SkillRef)
	}
	if res.PromptRef != nil {
		promptRef, _ = json.Marshal(res.PromptRef)
	}
	if _, err := tx.Exec(ctx, `UPDATE rounds SET status = 'publishing', verdict = $2, risk = $3, assessment = $4, skill_ref = $5, prompt_ref = $6, ledger_outcome = $7
		WHERE id = $1`, roundID, out.Verdict, risk, assessment, skillRef, promptRef, outRaw); err != nil {
		return Reconciled{}, err
	}
	return Reconciled{}, tx.Commit(ctx)
}

// Publish posts the round and sets it done (§7.2 step 5).
func (a *Activities) Publish(ctx context.Context, roundID uuid.UUID) error {
	if _, err := a.Publisher.Publish(ctx, roundID); err != nil {
		return err
	}
	_, err := a.Pool.Exec(ctx, `UPDATE rounds SET status = 'done', finished_at = coalesce(finished_at, now()) WHERE id = $1 AND status = 'publishing'`, roundID)
	return err
}

// ReportSkael sends the round to Skael (§7.2 step 6).
func (a *Activities) ReportSkael(ctx context.Context, roundID uuid.UUID) error {
	return a.Reporter.ReportRound(ctx, roundID)
}

// ReportReview sends the final outcomes of a closed review (§7.1 close).
func (a *Activities) ReportReview(ctx context.Context, reviewID uuid.UUID) error {
	rows, err := a.Pool.Query(ctx, `SELECT id FROM rounds WHERE review_id = $1 AND status IN ('done', 'failed', 'cancelled', 'budget_exceeded') ORDER BY created_at`, reviewID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		errs = append(errs, a.Reporter.ReportRound(ctx, id))
	}
	return errors.Join(errs...)
}

// ThreadState handles a person who resolves or unresolves a Sayso thread
// (§9.4): resolving an open finding sets it resolved_by_human.
func (a *Activities) ThreadState(ctx context.Context, reviewID uuid.UUID, threadNodeID string, commentID int64, resolved bool, actor string) error {
	return pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		var id uuid.UUID
		var status string
		err := tx.QueryRow(ctx, `SELECT id, status FROM findings WHERE review_id = $1 AND (thread_node_id = $2 OR comment_id = $3) FOR UPDATE`,
			reviewID, threadNodeID, commentID).Scan(&id, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if actor == "" {
			actor = "github"
		}
		switch {
		case resolved && status == findings.StatusOpen:
			return findings.SetStatus(ctx, tx, id, nil, findings.StatusResolvedByHuman, "resolved_by_human", actor, "thread resolved")
		case !resolved && status == findings.StatusResolvedByHuman:
			return findings.SetStatus(ctx, tx, id, nil, findings.StatusOpen, "reopened", actor, "thread unresolved")
		}
		return nil
	})
}

// SetCancelReason stores why the PRReview workflow cancels a round.
func (a *Activities) SetCancelReason(ctx context.Context, roundID uuid.UUID, reason string) error {
	_, err := a.Pool.Exec(ctx, `UPDATE rounds SET cancelled_by = $2 WHERE id = $1`, roundID, reason)
	return err
}

// Debounce reads the debounce setting in seconds (§7.1, default 90).
func (a *Activities) Debounce(ctx context.Context) (int, error) {
	return a.intSetting(ctx, settings.DebounceSeconds, 90), nil
}

// RoundTimeout reads the round timeout in minutes (default 30).
func (a *Activities) RoundTimeout(ctx context.Context, _ uuid.UUID) (int, error) {
	return a.intSetting(ctx, settings.RoundTimeoutMinutes, 30), nil
}

// ReplyTimeout reads the reply and fix run timeout in minutes (default 10).
func (a *Activities) ReplyTimeout(ctx context.Context, _ uuid.UUID) (int, error) {
	return a.intSetting(ctx, settings.ReplyTimeoutMinutes, 10), nil
}

// WorkKind reads whether a work item is a reply or a fix.
func (a *Activities) WorkKind(ctx context.Context, id uuid.UUID) (string, error) {
	var k string
	err := a.Pool.QueryRow(ctx, `SELECT kind FROM agent_work WHERE id = $1`, id).Scan(&k)
	return k, err
}

// RoundStatus reads the status of a round.
func (a *Activities) RoundStatus(ctx context.Context, roundID uuid.UUID) (string, error) {
	var s string
	err := a.Pool.QueryRow(ctx, `SELECT status FROM rounds WHERE id = $1`, roundID).Scan(&s)
	return s, err
}

// UpdateStatusLine renders the summary with the current state.
func (a *Activities) UpdateStatusLine(ctx context.Context, roundID uuid.UUID) error {
	return a.Publisher.StatusLine(ctx, roundID)
}
