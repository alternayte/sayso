package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// IssueComment is a pull request conversation comment.
type IssueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User User   `json:"user"`
}

// PullComment is a pull request review comment with its location.
type PullComment struct {
	ID        int64  `json:"id"`
	NodeID    string `json:"node_id"`
	Body      string `json:"body"`
	User      User   `json:"user"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	InReplyTo int64  `json:"in_reply_to_id"`
}

// PullFile is one changed file of a pull request with its patch.
type PullFile struct {
	Filename string `json:"filename"`
	Patch    string `json:"patch"`
}

// InlineComment is one comment of a review.
type InlineComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line,omitempty"`
	Side      string `json:"side"`
	Body      string `json:"body"`
}

const perPage = 100

// listAll reads every page of a list route.
func listAll[T any](ctx context.Context, c *Client, token string, inst int64, path string, prio Priority) ([]T, error) {
	var out []T
	for page := 1; ; page++ {
		var batch []T
		sep := "?"
		if u, err := url.Parse(path); err == nil && u.RawQuery != "" {
			sep = "&"
		}
		next, err := c.paged(ctx, call{method: http.MethodGet, path: fmt.Sprintf("%s%sper_page=%d&page=%d", path, sep, perPage, page), auth: "Bearer " + token, out: &batch, inst: inst, prio: prio})
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if next == "" || len(batch) < perPage {
			return out, nil
		}
	}
}

// IssueComments lists the conversation comments of a pull request.
func (c *Client) IssueComments(ctx context.Context, token string, inst int64, fullName string, number int) ([]IssueComment, error) {
	return listAll[IssueComment](ctx, c, token, inst, fmt.Sprintf("/repos/%s/issues/%d/comments", fullName, number), High)
}

// CreateIssueComment posts a conversation comment.
func (c *Client) CreateIssueComment(ctx context.Context, token string, inst int64, fullName string, number int, body string, prio Priority) (IssueComment, error) {
	var out IssueComment
	err := c.call(ctx, call{method: http.MethodPost, path: fmt.Sprintf("/repos/%s/issues/%d/comments", fullName, number), auth: "Bearer " + token,
		in: map[string]string{"body": body}, out: &out, inst: inst, prio: prio})
	return out, err
}

// EditIssueComment replaces the body of a conversation comment.
func (c *Client) EditIssueComment(ctx context.Context, token string, inst int64, fullName string, id int64, body string, prio Priority) error {
	return c.call(ctx, call{method: http.MethodPatch, path: fmt.Sprintf("/repos/%s/issues/comments/%d", fullName, id), auth: "Bearer " + token,
		in: map[string]string{"body": body}, inst: inst, prio: prio})
}

// AddReaction adds a reaction to a pull request. A repeat is harmless.
func (c *Client) AddReaction(ctx context.Context, token string, inst int64, fullName string, number int, content string) error {
	return c.call(ctx, call{method: http.MethodPost, path: fmt.Sprintf("/repos/%s/issues/%d/reactions", fullName, number), auth: "Bearer " + token,
		in: map[string]string{"content": content}, inst: inst, prio: High})
}

// PullFiles lists the changed files of a pull request.
func (c *Client) PullFiles(ctx context.Context, token string, inst int64, fullName string, number int) ([]PullFile, error) {
	return listAll[PullFile](ctx, c, token, inst, fmt.Sprintf("/repos/%s/pulls/%d/files", fullName, number), High)
}

// PullComments lists the review comments of a pull request.
func (c *Client) PullComments(ctx context.Context, token string, inst int64, fullName string, number int) ([]PullComment, error) {
	return listAll[PullComment](ctx, c, token, inst, fmt.Sprintf("/repos/%s/pulls/%d/comments", fullName, number), High)
}

// CreateCommentReview submits one review with event COMMENT. Sayso never
// submits APPROVE or REQUEST_CHANGES (SI-07), so the event is not a
// parameter.
func (c *Client) CreateCommentReview(ctx context.Context, token string, inst int64, fullName string, number int, commitID, body string, comments []InlineComment) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	in := map[string]any{"commit_id": commitID, "body": body, "event": "COMMENT", "comments": comments}
	err := c.call(ctx, call{method: http.MethodPost, path: fmt.Sprintf("/repos/%s/pulls/%d/reviews", fullName, number), auth: "Bearer " + token,
		in: in, out: &out, inst: inst, prio: High})
	return out.ID, err
}

// ReplyToComment replies in the thread of a review comment.
func (c *Client) ReplyToComment(ctx context.Context, token string, inst int64, fullName string, number int, commentID int64, body string) (PullComment, error) {
	var out PullComment
	err := c.call(ctx, call{method: http.MethodPost, path: fmt.Sprintf("/repos/%s/pulls/%d/comments/%d/replies", fullName, number, commentID),
		auth: "Bearer " + token, in: map[string]string{"body": body}, out: &out, inst: inst, prio: High})
	return out, err
}

// CreateCheckRun creates a check run.
func (c *Client) CreateCheckRun(ctx context.Context, token string, inst int64, fullName string, run CheckRun) (CheckRun, error) {
	var out CheckRun
	err := c.call(ctx, call{method: http.MethodPost, path: fmt.Sprintf("/repos/%s/check-runs", fullName), auth: "Bearer " + token, in: run, out: &out, inst: inst, prio: High})
	return out, err
}

// CheckRunsFor lists the check runs with a name on a commit.
func (c *Client) CheckRunsFor(ctx context.Context, token string, inst int64, fullName, sha, name string) ([]CheckRun, error) {
	var out struct {
		CheckRuns []CheckRun `json:"check_runs"`
	}
	err := c.call(ctx, call{method: http.MethodGet, path: fmt.Sprintf("/repos/%s/commits/%s/check-runs?check_name=%s&per_page=100", fullName, sha, url.QueryEscape(name)),
		auth: "Bearer " + token, out: &out, inst: inst, prio: High})
	return out.CheckRuns, err
}

// SetThreadResolved resolves or unresolves a review thread (GraphQL).
func (c *Client) SetThreadResolved(ctx context.Context, token string, inst int64, threadNodeID string, resolved bool) error {
	op := "resolveReviewThread"
	if !resolved {
		op = "unresolveReviewThread"
	}
	q := fmt.Sprintf(`mutation($threadId: ID!) { %s(input: {threadId: $threadId}) { thread { id isResolved } } }`, op)
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.call(ctx, call{method: http.MethodPost, path: "/graphql", auth: "Bearer " + token,
		in: map[string]any{"query": q, "variables": map[string]string{"threadId": threadNodeID}}, out: &out, inst: inst, prio: High}); err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("github graphql %s: %s", op, out.Errors[0].Message)
	}
	return nil
}

// ReviewThreadID returns the node ID of the review thread that a top
// comment starts (GraphQL; REST has no thread IDs).
func (c *Client) ReviewThreadID(ctx context.Context, token string, inst int64, fullName string, number int, commentID int64) (string, error) {
	owner, name, _ := strings.Cut(fullName, "/")
	q := `query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) { pullRequest(number: $number) {
    reviewThreads(first: 100, after: $after) { pageInfo { hasNextPage endCursor } nodes { id comments(first: 1) { nodes { databaseId } } } } } } }`
	var after any
	for {
		var out struct {
			Data struct {
				Repository struct {
					PullRequest struct {
						ReviewThreads struct {
							PageInfo struct {
								HasNextPage bool   `json:"hasNextPage"`
								EndCursor   string `json:"endCursor"`
							} `json:"pageInfo"`
							Nodes []struct {
								ID       string `json:"id"`
								Comments struct {
									Nodes []struct {
										DatabaseID int64 `json:"databaseId"`
									} `json:"nodes"`
								} `json:"comments"`
							} `json:"nodes"`
						} `json:"reviewThreads"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		vars := map[string]any{"owner": owner, "name": name, "number": number, "after": after}
		if err := c.call(ctx, call{method: http.MethodPost, path: "/graphql", auth: "Bearer " + token, in: map[string]any{"query": q, "variables": vars}, out: &out, inst: inst, prio: High}); err != nil {
			return "", err
		}
		if len(out.Errors) > 0 {
			return "", fmt.Errorf("github graphql reviewThreads: %s", out.Errors[0].Message)
		}
		t := out.Data.Repository.PullRequest.ReviewThreads
		for _, n := range t.Nodes {
			if len(n.Comments.Nodes) > 0 && n.Comments.Nodes[0].DatabaseID == commentID {
				return n.ID, nil
			}
		}
		if !t.PageInfo.HasNextPage {
			return "", fmt.Errorf("github: no review thread for comment %d", commentID)
		}
		after = t.PageInfo.EndCursor
	}
}
