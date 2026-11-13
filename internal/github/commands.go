package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Permission returns the repository permission of a person: admin,
// maintain, write, triage, read or none.
func (c *Client) Permission(ctx context.Context, token string, inst int64, fullName, login string) (string, error) {
	var out struct {
		Permission string `json:"permission"`
		RoleName   string `json:"role_name"`
	}
	err := c.call(ctx, call{method: http.MethodGet, path: fmt.Sprintf("/repos/%s/collaborators/%s/permission", fullName, url.PathEscape(login)),
		auth: "Bearer " + token, out: &out, inst: inst, prio: High})
	if out.RoleName == "maintain" || out.RoleName == "triage" {
		return out.RoleName, err
	}
	return out.Permission, err
}

// IssueComment reads one conversation comment.
func (c *Client) IssueComment(ctx context.Context, token string, inst int64, fullName string, id int64) (IssueComment, error) {
	var out IssueComment
	err := c.call(ctx, call{method: http.MethodGet, path: fmt.Sprintf("/repos/%s/issues/comments/%d", fullName, id), auth: "Bearer " + token, out: &out, inst: inst, prio: High})
	return out, err
}

// PullComment reads one review comment with its location.
func (c *Client) PullComment(ctx context.Context, token string, inst int64, fullName string, id int64) (PullComment, error) {
	var out PullComment
	err := c.call(ctx, call{method: http.MethodGet, path: fmt.Sprintf("/repos/%s/pulls/comments/%d", fullName, id), auth: "Bearer " + token, out: &out, inst: inst, prio: High})
	return out, err
}

// ReactToComment adds a reaction to a comment; source is issue or review.
func (c *Client) ReactToComment(ctx context.Context, token string, inst int64, fullName, source string, id int64, content string) error {
	kind := "issues"
	if source == "review" {
		kind = "pulls"
	}
	return c.call(ctx, call{method: http.MethodPost, path: fmt.Sprintf("/repos/%s/%s/comments/%d/reactions", fullName, kind, id), auth: "Bearer " + token,
		in: map[string]string{"content": content}, inst: inst, prio: High})
}

// FixBranchPrefix is the only branch prefix that Sayso writes to (SI-07).
const FixBranchPrefix = "sayso/fix/"

func fixBranch(branch string) error {
	if !strings.HasPrefix(branch, FixBranchPrefix) {
		return fmt.Errorf("github: Sayso writes only to %s* branches, not %s", FixBranchPrefix, branch)
	}
	return nil
}

// CreateBranch creates a sayso/fix/ branch at a commit.
func (c *Client) CreateBranch(ctx context.Context, token string, inst int64, fullName, branch, sha string) error {
	if err := fixBranch(branch); err != nil {
		return err
	}
	return c.call(ctx, call{method: http.MethodPost, path: fmt.Sprintf("/repos/%s/git/refs", fullName), auth: "Bearer " + token,
		in: map[string]string{"ref": "refs/heads/" + branch, "sha": sha}, inst: inst, prio: High})
}

// FileWithSHA reads a file at a ref with its blob SHA; found is false when
// the file does not exist.
func (c *Client) FileWithSHA(ctx context.Context, token string, inst int64, fullName, path, ref string) (data []byte, sha string, found bool, err error) {
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
	}
	p := fmt.Sprintf("/repos/%s/contents/%s?ref=%s", fullName, strings.TrimLeft(path, "/"), url.QueryEscape(ref))
	if err := c.call(ctx, call{method: http.MethodGet, path: p, auth: "Bearer " + token, out: &out, inst: inst, prio: High}); err != nil {
		if err == ErrNotFound {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	data, err = base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	return data, out.SHA, true, err
}

// PutFile commits one file to a sayso/fix/ branch. sha is the blob SHA of
// the file it replaces, or empty for a new file.
func (c *Client) PutFile(ctx context.Context, token string, inst int64, fullName, branch, path, message string, data []byte, sha string) error {
	if err := fixBranch(branch); err != nil {
		return err
	}
	in := map[string]string{"message": message, "content": base64.StdEncoding.EncodeToString(data), "branch": branch}
	if sha != "" {
		in["sha"] = sha
	}
	return c.call(ctx, call{method: http.MethodPut, path: fmt.Sprintf("/repos/%s/contents/%s", fullName, strings.TrimLeft(path, "/")), auth: "Bearer " + token,
		in: in, inst: inst, prio: High})
}

// CreatePull opens a pull request from a sayso/fix/ branch.
func (c *Client) CreatePull(ctx context.Context, token string, inst int64, fullName, title, body, head, base string) (PullRequest, error) {
	var out PullRequest
	if err := fixBranch(head); err != nil {
		return out, err
	}
	err := c.call(ctx, call{method: http.MethodPost, path: fmt.Sprintf("/repos/%s/pulls", fullName), auth: "Bearer " + token,
		in: map[string]string{"title": title, "body": body, "head": head, "base": base}, out: &out, inst: inst, prio: High})
	return out, err
}
