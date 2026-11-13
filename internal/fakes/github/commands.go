package github

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// permissions are the repository permissions of the acme people (SDD §22).
var permissions = map[string]string{"octo-admin": "admin", "dev-alice": "write", "dev-bob": "read"}

// CommentReaction is a reaction on a comment.
type CommentReaction struct {
	CommentID int64
	Content   string
	Author    string
}

// Push is one write to a branch: a ref creation or a file commit.
type Push struct {
	Repo   string
	Branch string
	SHA    string
}

func (f *Fake) commandRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/collaborators/{login}/permission", f.permission)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/comments/{id}/reactions", f.commentReaction)
	mux.HandleFunc("POST /repos/{owner}/{repo}/pulls/comments/{id}/reactions", f.commentReaction)
	mux.HandleFunc("POST /repos/{owner}/{repo}/git/refs", f.createRef)
	mux.HandleFunc("PUT /repos/{owner}/{repo}/contents/{path...}", f.putContents)
	mux.HandleFunc("POST /repos/{owner}/{repo}/pulls", f.createPull)
}

func (f *Fake) permission(w http.ResponseWriter, r *http.Request) {
	if !f.instCall(w, r) {
		return
	}
	p := permissions[r.PathValue("login")]
	if p == "" {
		p = "none"
	}
	writeJSON(w, http.StatusOK, map[string]any{"permission": p, "user": map[string]any{"login": r.PathValue("login")}})
}

func (f *Fake) getIssueComment(w http.ResponseWriter, r *http.Request) {
	if !f.instCall(w, r) {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f.mu.Lock()
	c := f.pub.issueComments[id]
	f.mu.Unlock()
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, issueJSON(c))
}

func (f *Fake) commentReaction(w http.ResponseWriter, r *http.Request) {
	if !f.writeCall(w, r) {
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f.mu.Lock()
	f.pub.commentReactions = append(f.pub.commentReactions, CommentReaction{CommentID: id, Content: in.Content, Author: f.appLogin()})
	f.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"content": in.Content})
}

// CommentReactions returns the reactions on comments.
func (f *Fake) CommentReactions() []CommentReaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CommentReaction(nil), f.pub.commentReactions...)
}

// Pushes returns the branch writes of clients, in order.
func (f *Fake) Pushes() []Push {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Push(nil), f.pub.pushes...)
}

func (f *Fake) createRef(w http.ResponseWriter, r *http.Request) {
	if !f.writeCall(w, r) {
		return
	}
	var in struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	branch, ok := strings.CutPrefix(in.Ref, "refs/heads/")
	f.mu.Lock()
	defer f.mu.Unlock()
	repo := f.repoOf(r)
	if repo == nil || !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Reference is invalid"})
		return
	}
	if _, err := f.git(repo.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Reference already exists"})
		return
	}
	if _, err := f.git(repo.dir, "branch", branch, in.SHA); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": err.Error()})
		return
	}
	f.pub.pushes = append(f.pub.pushes, Push{Repo: repo.FullName, Branch: branch, SHA: in.SHA})
	writeJSON(w, http.StatusCreated, map[string]any{"ref": in.Ref, "object": map[string]any{"sha": in.SHA}})
}

func (f *Fake) putContents(w http.ResponseWriter, r *http.Request) {
	if !f.writeCall(w, r) {
		return
	}
	var in struct {
		Message string `json:"message"`
		Content string `json:"content"`
		Branch  string `json:"branch"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	data, err := base64.StdEncoding.DecodeString(in.Content)
	if err != nil || in.Branch == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "content and branch are required"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	repo := f.repoOf(r)
	if repo == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	from, err := f.git(repo.dir, "rev-parse", "refs/heads/"+in.Branch)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Branch not found"})
		return
	}
	sha, err := f.commit(repo, in.Branch, strings.TrimSpace(from), map[string]string{r.PathValue("path"): string(data)}, in.Message)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}
	f.pub.pushes = append(f.pub.pushes, Push{Repo: repo.FullName, Branch: in.Branch, SHA: sha})
	writeJSON(w, http.StatusOK, map[string]any{"commit": map[string]any{"sha": sha}})
}

func (f *Fake) createPull(w http.ResponseWriter, r *http.Request) {
	if !f.writeCall(w, r) {
		return
	}
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	repo := f.repoOf(r)
	if repo == nil {
		f.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	head, err1 := f.git(repo.dir, "rev-parse", "refs/heads/"+in.Head)
	base, err2 := f.git(repo.dir, "rev-parse", "refs/heads/"+in.Base)
	if err1 != nil || err2 != nil {
		f.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "head or base branch not found"})
		return
	}
	n := repo.nextPR
	repo.nextPR++
	p := &Pull{Number: n, NodeID: fmt.Sprintf("PR_%d_%d", repo.ID, n), Author: f.appLogin(), State: "open", HeadSHA: strings.TrimSpace(head), HeadRef: in.Head,
		BaseSHA: strings.TrimSpace(base), BaseRef: in.Base, Title: in.Title, Body: in.Body}
	repo.Pulls[n] = p
	full := repo.FullName
	out := f.pullJSON(repo, p)
	f.mu.Unlock()
	// GitHub sends the opened event for a pull request that an App opens.
	go f.PullEvent(full, n, "opened", nil)
	writeJSON(w, http.StatusCreated, out)
}

// AddIssueComment stores a conversation comment by a person and sends the
// issue_comment event.
func (f *Fake) AddIssueComment(repo string, number int, author, body string) (int64, int) {
	f.mu.Lock()
	f.nextID++
	c := &IssueComment{ID: f.nextID, Repo: repo, Number: number, Body: body, Author: author}
	f.pub.issueComments[c.ID] = c
	r := f.repos[repo]
	payload := map[string]any{"action": "created", "comment": issueJSON(c), "repository": f.repoJSON(r), "sender": map[string]any{"login": author},
		"issue": map[string]any{"number": number, "pull_request": map[string]any{"url": "x"}}}
	f.mu.Unlock()
	_, code := f.Send("issue_comment", payload)
	return c.ID, code
}

// AddReviewComment stores a review comment by a person (a reply when
// inReplyTo is set) and sends the pull_request_review_comment event.
func (f *Fake) AddReviewComment(repo string, number int, author, body, path string, line int, inReplyTo int64) (int64, int) {
	f.mu.Lock()
	f.nextID++
	c := &Comment{ID: f.nextID, Repo: repo, Number: number, Body: body, Author: author, InReplyTo: inReplyTo, Path: path, Line: line}
	if parent := f.comments[inReplyTo]; parent != nil {
		c.Path, c.Line = parent.Path, parent.Line
	}
	f.comments[c.ID] = c
	r := f.repos[repo]
	p := r.Pulls[number]
	payload := map[string]any{"action": "created", "comment": f.commentJSON(c), "pull_request": f.pullJSON(r, p), "repository": f.repoJSON(r), "sender": map[string]any{"login": author}}
	f.mu.Unlock()
	_, code := f.Send("pull_request_review_comment", payload)
	return c.ID, code
}

// Thread returns the comments of the thread that a top comment starts, in order.
func (f *Fake) Thread(top int64) []Comment {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Comment
	for _, c := range f.comments {
		if c.ID == top || c.InReplyTo == top {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SetFork makes the head of a pull request come from a fork.
func (f *Fake) SetFork(repo string, number int) {
	f.mu.Lock()
	f.repos[repo].Pulls[number].Fork = true
	f.mu.Unlock()
}

// FileAt reads a file of a repository at a branch or commit.
func (f *Fake) FileAt(repo, path, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.git(f.repos[repo].dir, "show", ref+":"+path)
}
