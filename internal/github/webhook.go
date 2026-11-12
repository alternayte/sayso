package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.temporal.io/sdk/client"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/repos"
	"github.com/alternayte/sayso/internal/review"
)

// MaxWebhookBody is the largest payload GitHub sends (25 MB).
const MaxWebhookBody = 25 << 20

// Delivery headers of one webhook request.
type Headers struct {
	Event     string
	Delivery  string
	Signature string
}

// HeadersFrom reads the GitHub delivery headers.
func HeadersFrom(h http.Header) Headers {
	return Headers{Event: h.Get("X-GitHub-Event"), Delivery: h.Get("X-GitHub-Delivery"), Signature: h.Get("X-Hub-Signature-256")}
}

// Sign returns the X-Hub-Signature-256 value of body.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// ValidSignature checks X-Hub-Signature-256 in constant time.
func ValidSignature(secret string, body []byte, sig string) bool {
	return secret != "" && hmac.Equal([]byte(Sign(secret, body)), []byte(sig))
}

// Ingest verifies webhook deliveries, records each delivery once, keeps
// installations and repositories current and signals the PRReview
// workflows (§6.2, §6.3).
type Ingest struct {
	Pool     *pgxpool.Pool
	Secrets  SecretReader
	Temporal client.Client
	GitHub   func(ctx context.Context) (*Client, error)
	Tokens   *Tokens
	Log      *slog.Logger
	// Signaled observes the time from request start to the workflow signal.
	Signaled prometheus.Observer
}

// WebhookSecret returns the webhook secret of the App.
func (in *Ingest) WebhookSecret(ctx context.Context) (string, error) {
	id, err := New(in.Pool).WebhookSecretID(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoApp
	}
	if err != nil {
		return "", err
	}
	return in.Secrets.ValueByID(ctx, id)
}

// ServeHTTP is POST /webhooks/github (direct ingress).
func (in *Ingest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxWebhookBody))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	code, msg := in.Handle(r.Context(), "direct", HeadersFrom(r.Header), body, start)
	http.Error(w, msg, code)
}

// Handle processes one delivery from either ingress path and returns the
// HTTP status and a short message for GitHub.
func (in *Ingest) Handle(ctx context.Context, via string, h Headers, body []byte, start time.Time) (int, string) {
	secret, err := in.WebhookSecret(ctx)
	if err != nil && !errors.Is(err, ErrNoApp) {
		in.Log.Error("webhook secret", "error", err)
		return http.StatusServiceUnavailable, "webhook secret not available"
	}
	if !ValidSignature(secret, body, h.Signature) {
		_ = audit.Write(ctx, in.Pool, audit.Actor{Kind: "github", Login: "webhook"}, "webhook.signature_refused", h.Delivery,
			map[string]any{"event": h.Event, "via": via})
		return http.StatusUnauthorized, "signature check failed"
	}
	var head struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return http.StatusBadRequest, "body is not JSON"
	}
	if h.Delivery == "" || h.Event == "" {
		return http.StatusBadRequest, "delivery headers missing"
	}
	q := New(in.Pool)
	if re, err := q.IsRedelivery(ctx, h.Delivery); err == nil && re {
		via = "redelivery"
	}
	if _, err := q.InsertDelivery(ctx, InsertDeliveryParams{DeliveryID: h.Delivery, Event: h.Event, Action: head.Action, ReceivedVia: via}); errors.Is(err, pgx.ErrNoRows) {
		return http.StatusOK, "duplicate delivery"
	} else if err != nil {
		in.Log.Error("record delivery", "error", err)
		return http.StatusServiceUnavailable, "database error"
	}
	if err := in.dispatch(ctx, h, body, start); err != nil {
		// Forget the delivery so that a redelivery is handled again.
		in.Log.Error("webhook handling failed", "delivery", h.Delivery, "event", h.Event, "error", err)
		_ = q.DeleteDelivery(context.WithoutCancel(ctx), h.Delivery)
		return http.StatusInternalServerError, "handling failed"
	}
	if err := q.FinishDelivery(ctx, FinishDeliveryParams{DeliveryID: h.Delivery}); err != nil {
		in.Log.Warn("finish delivery", "error", err)
	}
	return http.StatusOK, "ok"
}

type ghRepo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

type payload struct {
	Action       string `json:"action"`
	Installation *struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	} `json:"installation"`
	Repositories        []ghRepo    `json:"repositories"`
	RepositoriesAdded   []ghRepo    `json:"repositories_added"`
	RepositoriesRemoved []ghRepo    `json:"repositories_removed"`
	Repository          *ghRepo     `json:"repository"`
	Sender              User        `json:"sender"`
	Before              string      `json:"before"`
	After               string      `json:"after"`
	PullRequest         *pullEvent  `json:"pull_request"`
	Issue               *issueEvent `json:"issue"`
	Comment             *struct {
		ID        int64  `json:"id"`
		Body      string `json:"body"`
		User      User   `json:"user"`
		InReplyTo int64  `json:"in_reply_to_id"`
	} `json:"comment"`
	Thread *struct {
		NodeID   string `json:"node_id"`
		Comments []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
			User User   `json:"user"`
		} `json:"comments"`
	} `json:"thread"`
	CheckRun *struct {
		Name    string `json:"name"`
		HeadSHA string `json:"head_sha"`
		App     struct {
			ID int64 `json:"id"`
		} `json:"app"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_run"`
}

type pullEvent struct {
	Number int    `json:"number"`
	Draft  bool   `json:"draft"`
	Merged bool   `json:"merged"`
	User   User   `json:"user"`
	NodeID string `json:"node_id"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
}

type issueEvent struct {
	Number      int       `json:"number"`
	PullRequest *struct{} `json:"pull_request"`
}

// CommandPrefix starts every Sayso command.
const CommandPrefix = "/sayso"

// FindingMarker starts the marker at the end of each finding comment.
const FindingMarker = "<!-- sayso:finding:"

func isCommand(body string) bool {
	b := strings.TrimSpace(body)
	return b == CommandPrefix || strings.HasPrefix(b, CommandPrefix+" ") || strings.HasPrefix(b, CommandPrefix+"\n")
}

func (in *Ingest) dispatch(ctx context.Context, h Headers, body []byte, start time.Time) error {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return fmt.Errorf("parse %s: %w", h.Event, err)
	}
	switch h.Event {
	case "installation", "installation_repositories":
		return in.installation(ctx, h.Event, &p)
	case "ping":
		return nil
	}
	if p.Repository == nil || p.Installation == nil {
		return nil
	}
	repo, err := repos.New(in.Pool).ByGitHubID(ctx, p.Repository.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		in.Log.Info("event for an unknown repository", "repository", p.Repository.FullName, "event", h.Event)
		return nil
	}
	if err != nil {
		return err
	}
	if p.Repository.DefaultBranch != "" {
		if err := repos.New(in.Pool).SetDefaultBranch(ctx, repos.SetDefaultBranchParams{GithubID: repo.GithubID, DefaultBranch: p.Repository.DefaultBranch}); err != nil {
			return err
		}
	}
	if repo.Mode == "off" {
		return nil
	}
	app, err := LoadApp(ctx, in.Pool, in.Secrets)
	if err != nil {
		return err
	}
	bot := app.Slug + "[bot]"
	ev := event{in: in, ctx: ctx, repo: repo, inst: p.Installation.ID, app: app, delivery: h.Delivery, start: start}
	switch h.Event {
	case "pull_request":
		pr := p.PullRequest
		if pr == nil {
			return nil
		}
		ev.number = pr.Number
		switch p.Action {
		case "opened", "reopened", "ready_for_review":
			if pr.Draft || strings.EqualFold(pr.User.Login, bot) {
				return nil
			}
			trigger := review.TriggerOpened
			if p.Action == "ready_for_review" {
				trigger = review.TriggerReady
			}
			return ev.signal(review.SignalHeadUpdated, review.Signal{Trigger: trigger, HeadSHA: pr.Head.SHA, BaseSHA: pr.Base.SHA})
		case "synchronize":
			if pr.Draft || strings.EqualFold(pr.User.Login, bot) {
				return nil
			}
			forced, err := ev.forced(p.Before, p.After)
			if err != nil {
				return err
			}
			return ev.signal(review.SignalHeadUpdated, review.Signal{Trigger: review.TriggerPush, HeadSHA: pr.Head.SHA, BaseSHA: pr.Base.SHA, Forced: forced})
		case "converted_to_draft":
			return ev.signal(review.SignalPausedDraft, review.Signal{})
		case "closed":
			return ev.signal(review.SignalClosed, review.Signal{Merged: pr.Merged})
		}
	case "issue_comment":
		if p.Action != "created" || p.Issue == nil || p.Issue.PullRequest == nil || p.Comment == nil || strings.EqualFold(p.Comment.User.Login, bot) {
			return nil
		}
		if isCommand(p.Comment.Body) {
			ev.number = p.Issue.Number
			return ev.signal(review.SignalCommand, review.Signal{CommentID: p.Comment.ID, CommentSource: "issue"})
		}
	case "pull_request_review_comment":
		if p.Action != "created" || p.PullRequest == nil || p.Comment == nil || strings.EqualFold(p.Comment.User.Login, bot) {
			return nil
		}
		ev.number = p.PullRequest.Number
		c := p.Comment
		if isCommand(c.Body) {
			return ev.signal(review.SignalCommand, review.Signal{CommentID: c.ID, CommentSource: "review", InReplyTo: c.InReplyTo})
		}
		if c.InReplyTo != 0 {
			ours, err := ev.saysoComment(c.InReplyTo, bot)
			if err != nil {
				return err
			}
			if ours {
				return ev.signal(review.SignalThreadReply, review.Signal{CommentID: c.ID, CommentSource: "review", InReplyTo: c.InReplyTo})
			}
		}
		if strings.HasSuffix(c.User.Login, "[bot]") {
			return nil
		}
		return ev.signal(review.SignalHumanComment, review.Signal{CommentID: c.ID, CommentSource: "review", InReplyTo: c.InReplyTo})
	case "pull_request_review_thread":
		if (p.Action != "resolved" && p.Action != "unresolved") || p.PullRequest == nil || p.Thread == nil || len(p.Thread.Comments) == 0 {
			return nil
		}
		first := p.Thread.Comments[0]
		if !strings.EqualFold(first.User.Login, bot) || !strings.Contains(first.Body, FindingMarker) {
			return nil
		}
		ev.number = p.PullRequest.Number
		return ev.signal(review.SignalThreadState, review.Signal{ThreadNodeID: p.Thread.NodeID, CommentID: first.ID, Resolved: p.Action == "resolved"})
	case "check_run":
		cr := p.CheckRun
		if p.Action != "rerequested" || cr == nil || cr.App.ID != app.AppID || cr.Name != CheckName || len(cr.PullRequests) == 0 {
			return nil
		}
		ev.number = cr.PullRequests[0].Number
		return ev.signal(review.SignalHeadUpdated, review.Signal{Trigger: review.TriggerCheckRerequest, HeadSHA: cr.HeadSHA})
	}
	return nil
}

// CheckName is the name of the Sayso check run (§10.4).
const CheckName = "sayso"

type event struct {
	in       *Ingest
	ctx      context.Context
	repo     repos.Repository
	inst     int64
	app      *App
	number   int
	delivery string
	start    time.Time
}

func (e *event) signal(name string, s review.Signal) error {
	s.DeliveryID = e.delivery
	return SignalReview(e.ctx, e.in.Temporal, e.repo, e.number, name, s, func() {
		if e.in.Signaled != nil && !e.start.IsZero() {
			e.in.Signaled.Observe(time.Since(e.start).Seconds())
		}
	})
}

// SignalReview sends one signal to the PRReview workflow of a pull request
// with signal-with-start (§6.3).
func SignalReview(ctx context.Context, tc client.Client, repo repos.Repository, number int, name string, s review.Signal, done func()) error {
	id := review.WorkflowID(repo.GithubID, number)
	_, err := tc.SignalWithStartWorkflow(ctx, id, name, s, client.StartWorkflowOptions{
		ID: id, TaskQueue: review.TaskQueue,
	}, review.WorkflowType, review.Start{RepositoryID: uuid.UUID(repo.ID), RepoGitHubID: repo.GithubID, Number: number})
	if err != nil {
		return fmt.Errorf("signal %s to %s: %w", name, id, err)
	}
	if done != nil {
		done()
	}
	return nil
}

func (e *event) token() (*Client, string, error) {
	gh, err := e.in.GitHub(e.ctx)
	if err != nil {
		return nil, "", err
	}
	tok, err := e.in.Tokens.Get(e.ctx, gh, e.app, e.inst)
	return gh, tok, err
}

// forced reports whether a push replaced history: before is not an ancestor
// of after.
func (e *event) forced(before, after string) (bool, error) {
	if before == "" || strings.Trim(before, "0") == "" {
		return false, nil
	}
	gh, tok, err := e.token()
	if err != nil {
		return false, err
	}
	anc, err := gh.IsAncestor(e.ctx, tok, e.inst, e.repo.FullName, before, after)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	return !anc && err == nil, err
}

// saysoComment reports whether a review comment is a Sayso finding comment.
func (e *event) saysoComment(id int64, bot string) (bool, error) {
	gh, tok, err := e.token()
	if err != nil {
		return false, err
	}
	c, err := gh.GetReviewComment(e.ctx, tok, e.inst, e.repo.FullName, id)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.EqualFold(c.User.Login, bot) && strings.Contains(c.Body, FindingMarker), nil
}

func (in *Ingest) installation(ctx context.Context, event string, p *payload) error {
	if p.Installation == nil {
		return nil
	}
	inst := p.Installation.ID
	return pgx.BeginFunc(ctx, in.Pool, func(tx pgx.Tx) error {
		gq, rq := New(tx), repos.New(tx)
		add := func(list []ghRepo) error {
			for _, r := range list {
				id, err := uuid.NewV7()
				if err != nil {
					return err
				}
				if _, err := rq.UpsertRepository(ctx, repos.UpsertRepositoryParams{ID: id, GithubID: r.ID, InstallationID: inst, FullName: r.FullName, Private: r.Private}); err != nil {
					return err
				}
			}
			return nil
		}
		actor := audit.Actor{Kind: "github", Login: p.Sender.Login}
		switch {
		case event == "installation" && p.Action == "deleted":
			if err := rq.RemoveInstallationRepositories(ctx, inst); err != nil {
				return err
			}
			return audit.Write(ctx, tx, actor, "github.installation_deleted", p.Installation.Account.Login, map[string]any{"installation_id": inst})
		case event == "installation" && (p.Action == "suspend" || p.Action == "unsuspend"):
			var at *time.Time
			if p.Action == "suspend" {
				now := time.Now()
				at = &now
			}
			return gq.SetInstallationSuspended(ctx, SetInstallationSuspendedParams{InstallationID: inst, SuspendedAt: at})
		}
		if err := gq.UpsertInstallation(ctx, UpsertInstallationParams{InstallationID: inst, AccountLogin: p.Installation.Account.Login}); err != nil {
			return err
		}
		if err := add(p.Repositories); err != nil {
			return err
		}
		if err := add(p.RepositoriesAdded); err != nil {
			return err
		}
		for _, r := range p.RepositoriesRemoved {
			if err := rq.RemoveRepository(ctx, r.ID); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, actor, "github."+event+"."+p.Action, p.Installation.Account.Login, map[string]any{
			"installation_id": inst, "added": names(append(p.Repositories, p.RepositoriesAdded...)), "removed": names(p.RepositoriesRemoved),
		})
	})
}

func names(rs []ghRepo) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.FullName)
	}
	return out
}
