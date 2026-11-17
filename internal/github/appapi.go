package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Conversion is the App that the manifest flow created.
type Conversion struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	HTMLURL       string `json:"html_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// ConvertManifest exchanges the code of the manifest flow for the App
// credentials (REQ-GH-001).
func (c *Client) ConvertManifest(ctx context.Context, code string) (*Conversion, error) {
	var out Conversion
	err := c.call(ctx, call{method: http.MethodPost, path: "/app-manifests/" + url.PathEscape(code) + "/conversions", out: &out})
	if err != nil {
		return nil, err
	}
	if out.ID == 0 || out.PEM == "" || out.WebhookSecret == "" {
		return nil, fmt.Errorf("github: the manifest conversion returned no App credentials")
	}
	return &out, nil
}

// TokenRequest narrows an installation token. Empty fields keep the
// installation's repositories and permissions.
type TokenRequest struct {
	Repositories []string          `json:"repositories,omitempty"`
	Permissions  map[string]string `json:"permissions,omitempty"`
}

// ReadOnlyRun is the permission set of the token for an agent run (§6.1).
var ReadOnlyRun = map[string]string{"contents": "read", "pull_requests": "read", "metadata": "read"}

// InstallationToken mints a token for one installation.
func (c *Client) InstallationToken(ctx context.Context, app *App, inst int64, req TokenRequest) (string, error) {
	tok, _, err := c.installationToken(ctx, app, inst, req)
	return tok, err
}

func (c *Client) installationToken(ctx context.Context, app *App, inst int64, req TokenRequest) (string, time.Time, error) {
	jwt, err := c.appJWT(app)
	if err != nil {
		return "", time.Time{}, err
	}
	var tok struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	var in any
	if len(req.Repositories) > 0 || len(req.Permissions) > 0 {
		in = req
	}
	if err := c.call(ctx, call{method: http.MethodPost, path: fmt.Sprintf("/app/installations/%d/access_tokens", inst), auth: "Bearer " + jwt, in: in, out: &tok}); err != nil {
		return "", time.Time{}, fmt.Errorf("github: installation token: %w", err)
	}
	return tok.Token, tok.ExpiresAt, nil
}

// tokenLife is how long GitHub keeps an installation token when a response
// gives no expires_at.
const tokenLife = time.Hour

// Tokens caches installation tokens until Margin before they expire (§6.1).
// Agent run tokens are never cached; use Client.InstallationToken for them.
type Tokens struct {
	Margin time.Duration
	Now    func() time.Time
	mu     sync.Mutex
	by     map[int64]cached
}

type cached struct {
	token   string
	expires time.Time
}

// NewTokens returns a cache that renews a token 10 minutes before it expires.
func NewTokens() *Tokens {
	return &Tokens{Margin: 10 * time.Minute, Now: time.Now, by: map[int64]cached{}}
}

// wall is the current wall-clock time. The monotonic reading is stripped:
// the monotonic clock stops while the host sleeps, GitHub's clock does not.
func (t *Tokens) wall() time.Time { return t.Now().Round(0) }

// Get returns a cached token of the installation, or mints a new one.
func (t *Tokens) Get(ctx context.Context, c *Client, app *App, inst int64) (string, error) {
	t.mu.Lock()
	e, ok := t.by[inst]
	t.mu.Unlock()
	if ok && t.wall().Before(e.expires.Add(-t.Margin)) {
		return e.token, nil
	}
	return t.mint(ctx, c, app, inst)
}

// Renew drops stale from the cache when it is the cached token, and returns
// a valid token. Client calls it after a 401.
func (t *Tokens) Renew(ctx context.Context, c *Client, app *App, inst int64, stale string) (string, error) {
	t.mu.Lock()
	if e, ok := t.by[inst]; ok && e.token == stale {
		delete(t.by, inst)
	}
	t.mu.Unlock()
	return t.Get(ctx, c, app, inst)
}

func (t *Tokens) mint(ctx context.Context, c *Client, app *App, inst int64) (string, error) {
	start := t.wall()
	tok, expires, err := c.installationToken(ctx, app, inst, TokenRequest{})
	if err != nil {
		return "", err
	}
	if expires.IsZero() {
		expires = start.Add(tokenLife)
	}
	t.mu.Lock()
	t.by[inst] = cached{token: tok, expires: expires}
	t.mu.Unlock()
	return tok, nil
}

// Delivery is one App webhook delivery as GitHub lists it.
type Delivery struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	DeliveredAt time.Time `json:"delivered_at"`
	Redelivery  bool      `json:"redelivery"`
	Status      string    `json:"status"`
	StatusCode  int       `json:"status_code"`
	Event       string    `json:"event"`
	Action      string    `json:"action"`
}

// Failed reports whether GitHub could not deliver it.
func (d Delivery) Failed() bool { return d.StatusCode == 0 || d.StatusCode >= 400 }

// Deliveries lists the App webhook deliveries since a time, newest first.
func (c *Client) Deliveries(ctx context.Context, app *App, since time.Time) ([]Delivery, error) {
	jwt, err := c.appJWT(app)
	if err != nil {
		return nil, err
	}
	var all []Delivery
	cursor := ""
	for {
		path := "/app/hook/deliveries?per_page=100"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var page []Delivery
		next, err := c.paged(ctx, call{method: http.MethodGet, path: path, auth: "Bearer " + jwt, out: &page, prio: Low})
		if err != nil {
			return nil, err
		}
		for _, d := range page {
			if d.DeliveredAt.Before(since) {
				return all, nil
			}
			all = append(all, d)
		}
		if next == "" || len(page) == 0 {
			return all, nil
		}
		cursor = next
	}
}

// Redeliver asks GitHub to send a delivery again.
func (c *Client) Redeliver(ctx context.Context, app *App, id int64) error {
	jwt, err := c.appJWT(app)
	if err != nil {
		return err
	}
	return c.call(ctx, call{method: http.MethodPost, path: fmt.Sprintf("/app/hook/deliveries/%d/attempts", id), auth: "Bearer " + jwt, prio: Low})
}

// paged runs a GET and returns the cursor of the next page from the Link header.
func (c *Client) paged(ctx context.Context, k call) (string, error) {
	var next string
	k.onResponse = func(resp *http.Response) { next = nextCursor(resp.Header.Get("Link")) }
	err := c.call(ctx, k)
	return next, err
}

func nextCursor(link string) string {
	for _, part := range strings.Split(link, ",") {
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start, end := strings.Index(part, "<"), strings.Index(part, ">")
		if start < 0 || end < start {
			continue
		}
		u, err := url.Parse(part[start+1 : end])
		if err != nil {
			continue
		}
		if c := u.Query().Get("cursor"); c != "" {
			return c
		}
		return u.Query().Get("page")
	}
	return ""
}

// PullRequest is the part of a pull request that Sayso reads.
type PullRequest struct {
	Number int    `json:"number"`
	NodeID string `json:"node_id"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Draft  bool   `json:"draft"`
	State  string `json:"state"`
	User   User   `json:"user"`
	Head   struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo *struct {
			ID int64 `json:"id"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// OpenPulls lists the open pull requests of a repository (low priority).
func (c *Client) OpenPulls(ctx context.Context, token string, inst int64, fullName string) ([]PullRequest, error) {
	var all []PullRequest
	for page := 1; ; page++ {
		var out []PullRequest
		path := fmt.Sprintf("/repos/%s/pulls?state=open&per_page=100&page=%d", fullName, page)
		if err := c.call(ctx, call{method: http.MethodGet, path: path, auth: "Bearer " + token, out: &out, inst: inst, prio: Low}); err != nil {
			return nil, err
		}
		all = append(all, out...)
		if len(out) < 100 {
			return all, nil
		}
	}
}

// IsAncestor reports whether base is an ancestor of head.
func (c *Client) IsAncestor(ctx context.Context, token string, inst int64, fullName, base, head string) (bool, error) {
	var out struct {
		Status string `json:"status"`
	}
	path := fmt.Sprintf("/repos/%s/compare/%s...%s", fullName, url.PathEscape(base), url.PathEscape(head))
	err := c.call(ctx, call{method: http.MethodGet, path: path, auth: "Bearer " + token, out: &out, inst: inst})
	if err != nil {
		return false, err
	}
	return out.Status == "ahead" || out.Status == "identical", nil
}

// ReviewComment is a pull request review comment.
type ReviewComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	User      User   `json:"user"`
	InReplyTo int64  `json:"in_reply_to_id"`
}

// GetReviewComment reads one review comment.
func (c *Client) GetReviewComment(ctx context.Context, token string, inst int64, fullName string, id int64) (*ReviewComment, error) {
	var out ReviewComment
	err := c.call(ctx, call{method: http.MethodGet, path: fmt.Sprintf("/repos/%s/pulls/comments/%d", fullName, id), auth: "Bearer " + token, out: &out, inst: inst})
	return &out, err
}

// CheckRun is the part of a check run that Sayso writes.
type CheckRun struct {
	ID         int64        `json:"id,omitempty"`
	Name       string       `json:"name"`
	HeadSHA    string       `json:"head_sha"`
	ExternalID string       `json:"external_id,omitempty"`
	Status     string       `json:"status,omitempty"`
	Conclusion string       `json:"conclusion,omitempty"`
	Output     *CheckOutput `json:"output,omitempty"`
}

// CheckOutput is the output of a check run.
type CheckOutput struct {
	Title       string       `json:"title"`
	Summary     string       `json:"summary"`
	Annotations []Annotation `json:"annotations,omitempty"`
}

// Annotation marks lines of a file in a check run.
type Annotation struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	AnnotationLevel string `json:"annotation_level"`
	Title           string `json:"title,omitempty"`
	Message         string `json:"message"`
}

// UpdateCheckRun changes a check run. Check run updates go first when the
// rate limit is low.
func (c *Client) UpdateCheckRun(ctx context.Context, token string, inst int64, fullName string, run CheckRun) error {
	return c.call(ctx, call{method: http.MethodPatch, path: fmt.Sprintf("/repos/%s/check-runs/%d", fullName, run.ID), auth: "Bearer " + token, in: run, inst: inst, prio: High})
}

// FileAt reads one file of a repository at a commit. It returns ErrNotFound
// when the file does not exist.
func (c *Client) FileAt(ctx context.Context, token string, inst int64, fullName, path, ref string) ([]byte, error) {
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	p := contentsPath(fullName, path) + "?ref=" + url.QueryEscape(ref)
	if err := c.call(ctx, call{method: http.MethodGet, path: p, auth: "Bearer " + token, out: &out, inst: inst}); err != nil {
		return nil, err
	}
	if out.Encoding != "base64" {
		return nil, fmt.Errorf("github: %s: encoding %q", path, out.Encoding)
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
}
