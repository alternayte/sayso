package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// IssueComment is a pull request conversation comment.
type IssueComment struct {
	ID     int64
	Repo   string
	Number int
	Body   string
	Author string
	// Edits counts the PATCH calls on the comment.
	Edits int
}

// Review is a pull request review that a client submitted.
type Review struct {
	ID       int64
	Repo     string
	Number   int
	CommitID string
	Event    string
	Body     string
	Comments []int64
}

// Reaction is a reaction on a pull request.
type Reaction struct {
	Repo    string
	Number  int
	Content string
	Author  string
}

// publishState is the GitHub state that Publish writes.
type publishState struct {
	issueComments map[int64]*IssueComment
	reviews       []*Review
	reactions     []Reaction
	// failWrite makes the n-th write call from now answer 502, once.
	failWrite int
}

func (f *Fake) appLogin() string { return f.AppSlug + "[bot]" }

// FailWrite makes the n-th write call (POST, PATCH, PUT, DELETE) from now
// fail with 502 once.
func (f *Fake) FailWrite(n int) {
	f.mu.Lock()
	f.pub.failWrite = n
	f.mu.Unlock()
}

// writeCall checks the token and the write failure switch.
func (f *Fake) writeCall(w http.ResponseWriter, r *http.Request) bool {
	if !f.instCall(w, r) {
		return false
	}
	f.mu.Lock()
	fail := false
	if f.pub.failWrite > 0 {
		f.pub.failWrite--
		fail = f.pub.failWrite == 0
	}
	f.mu.Unlock()
	if fail {
		writeJSON(w, http.StatusBadGateway, map[string]string{"message": "Server Error"})
		return false
	}
	return true
}

// IssueComments returns the conversation comments of a pull request.
func (f *Fake) IssueComments(repo string, number int) []IssueComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []IssueComment
	for _, c := range f.pub.issueComments {
		if c.Repo == repo && c.Number == number {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Reviews returns the submitted reviews, in order.
func (f *Fake) Reviews() []Review {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Review, 0, len(f.pub.reviews))
	for _, r := range f.pub.reviews {
		out = append(out, *r)
	}
	return out
}

// Reactions returns the reactions on pull requests.
func (f *Fake) Reactions() []Reaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Reaction(nil), f.pub.reactions...)
}

// ReviewComments returns the review comments of a repository, by ID.
func (f *Fake) ReviewComments(repo string) []Comment {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Comment
	for _, c := range f.comments {
		if c.Repo == repo {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (f *Fake) publishRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/{n}/comments", f.listIssueComments)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{n}/comments", f.createIssueComment)
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/issues/comments/{id}", f.editIssueComment)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{n}/reactions", f.createReaction)
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{n}", f.getPull)
	// One pattern for GET /pulls/{n}/{sub}: it overlaps /pulls/comments/{id}.
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{n}/{sub}", func(w http.ResponseWriter, r *http.Request) {
		switch n, sub := r.PathValue("n"), r.PathValue("sub"); {
		case n == "comments":
			r.SetPathValue("id", sub)
			f.getComment(w, r)
		case sub == "files":
			f.pullFiles(w, r)
		case sub == "reviews":
			f.listReviews(w, r)
		case sub == "comments":
			f.listReviewComments(w, r)
		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		}
	})
	mux.HandleFunc("POST /repos/{owner}/{repo}/pulls/{n}/reviews", f.createReview)
	mux.HandleFunc("POST /repos/{owner}/{repo}/pulls/{n}/comments/{id}/replies", f.reply)
	mux.HandleFunc("POST /repos/{owner}/{repo}/check-runs", f.createCheckRun)
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{ref}/check-runs", f.listCheckRuns)
	mux.HandleFunc("POST /graphql", f.graphql)
}

func issueJSON(c *IssueComment) map[string]any {
	return map[string]any{"id": c.ID, "body": c.Body, "user": map[string]any{"login": c.Author}}
}

func (f *Fake) listIssueComments(w http.ResponseWriter, r *http.Request) {
	if !f.instCall(w, r) {
		return
	}
	n, _ := strconv.Atoi(r.PathValue("n"))
	out := []map[string]any{}
	for _, c := range f.IssueComments(r.PathValue("owner")+"/"+r.PathValue("repo"), n) {
		out = append(out, issueJSON(&c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) createIssueComment(w http.ResponseWriter, r *http.Request) {
	if !f.writeCall(w, r) {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	n, _ := strconv.Atoi(r.PathValue("n"))
	f.mu.Lock()
	f.nextID++
	c := &IssueComment{ID: f.nextID, Repo: r.PathValue("owner") + "/" + r.PathValue("repo"), Number: n, Body: in.Body, Author: f.appLogin()}
	f.pub.issueComments[c.ID] = c
	f.mu.Unlock()
	writeJSON(w, http.StatusCreated, issueJSON(c))
}

func (f *Fake) editIssueComment(w http.ResponseWriter, r *http.Request) {
	if !f.writeCall(w, r) {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f.mu.Lock()
	c := f.pub.issueComments[id]
	if c != nil {
		c.Body = in.Body
		c.Edits++
	}
	f.mu.Unlock()
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, issueJSON(c))
}

func (f *Fake) createReaction(w http.ResponseWriter, r *http.Request) {
	if !f.writeCall(w, r) {
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	n, _ := strconv.Atoi(r.PathValue("n"))
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	f.mu.Lock()
	status := http.StatusCreated
	for _, x := range f.pub.reactions {
		if x.Repo == repo && x.Number == n && x.Content == in.Content && x.Author == f.appLogin() {
			status = http.StatusOK
		}
	}
	if status == http.StatusCreated {
		f.pub.reactions = append(f.pub.reactions, Reaction{Repo: repo, Number: n, Content: in.Content, Author: f.appLogin()})
	}
	f.mu.Unlock()
	writeJSON(w, status, map[string]any{"content": in.Content})
}

func (f *Fake) getPull(w http.ResponseWriter, r *http.Request) {
	if !f.instCall(w, r) {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	f.mu.Lock()
	defer f.mu.Unlock()
	repo := f.repoOf(r)
	if err != nil || repo == nil || repo.Pulls[n] == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, f.pullJSON(repo, repo.Pulls[n]))
}

// pullFiles lists the changed files with their patches, from git.
func (f *Fake) pullFiles(w http.ResponseWriter, r *http.Request) {
	if !f.instCall(w, r) {
		return
	}
	n, _ := strconv.Atoi(r.PathValue("n"))
	f.mu.Lock()
	repo := f.repoOf(r)
	var p *Pull
	if repo != nil {
		p = repo.Pulls[n]
	}
	f.mu.Unlock()
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	names, err := f.git(repo.dir, "diff", "--name-only", p.BaseSHA+"..."+p.HeadSHA)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}
	out := []map[string]any{}
	for _, name := range strings.Fields(names) {
		patch, _ := f.git(repo.dir, "diff", "--unified=3", p.BaseSHA+"..."+p.HeadSHA, "--", name)
		if i := strings.Index(patch, "\n@@"); i >= 0 {
			patch = patch[i+1:]
		}
		out = append(out, map[string]any{"filename": name, "status": "modified", "patch": strings.TrimRight(patch, "\n")})
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) commentJSON(c *Comment) map[string]any {
	return map[string]any{"id": c.ID, "node_id": fmt.Sprintf("PRRC_%d", c.ID), "body": c.Body, "user": map[string]any{"login": c.Author},
		"in_reply_to_id": c.InReplyTo, "path": c.Path, "line": c.Line, "start_line": c.StartLine, "commit_id": c.CommitID,
		"pull_request_review_id": c.ReviewID}
}

func (f *Fake) listReviews(w http.ResponseWriter, r *http.Request) {
	if !f.instCall(w, r) {
		return
	}
	n, _ := strconv.Atoi(r.PathValue("n"))
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	out := []map[string]any{}
	for _, v := range f.Reviews() {
		if v.Repo == repo && v.Number == n {
			out = append(out, map[string]any{"id": v.ID, "body": v.Body, "commit_id": v.CommitID, "state": v.Event, "user": map[string]any{"login": f.appLogin()}})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) createReview(w http.ResponseWriter, r *http.Request) {
	if !f.writeCall(w, r) {
		return
	}
	var in struct {
		CommitID string `json:"commit_id"`
		Body     string `json:"body"`
		Event    string `json:"event"`
		Comments []struct {
			Path      string `json:"path"`
			Line      int    `json:"line"`
			StartLine int    `json:"start_line"`
			Side      string `json:"side"`
			Body      string `json:"body"`
		} `json:"comments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": err.Error()})
		return
	}
	n, _ := strconv.Atoi(r.PathValue("n"))
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	f.mu.Lock()
	f.nextID++
	v := &Review{ID: f.nextID, Repo: repo, Number: n, CommitID: in.CommitID, Event: in.Event, Body: in.Body}
	for _, c := range in.Comments {
		f.nextID++
		f.comments[f.nextID] = &Comment{ID: f.nextID, Repo: repo, Number: n, Body: c.Body, Author: f.appLogin(), Path: c.Path, Line: c.Line,
			StartLine: c.StartLine, CommitID: in.CommitID, ReviewID: v.ID}
		v.Comments = append(v.Comments, f.nextID)
	}
	f.pub.reviews = append(f.pub.reviews, v)
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"id": v.ID, "state": in.Event})
}

func (f *Fake) listReviewComments(w http.ResponseWriter, r *http.Request) {
	if !f.instCall(w, r) {
		return
	}
	n, _ := strconv.Atoi(r.PathValue("n"))
	out := []map[string]any{}
	for _, c := range f.ReviewComments(r.PathValue("owner") + "/" + r.PathValue("repo")) {
		if c.Number == n {
			out = append(out, f.commentJSON(&c))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) reply(w http.ResponseWriter, r *http.Request) {
	if !f.writeCall(w, r) {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	n, _ := strconv.Atoi(r.PathValue("n"))
	f.mu.Lock()
	parent := f.comments[id]
	var c *Comment
	if parent != nil {
		f.nextID++
		c = &Comment{ID: f.nextID, Repo: parent.Repo, Number: n, Body: in.Body, Author: f.appLogin(), InReplyTo: id, Path: parent.Path, Line: parent.Line}
		f.comments[c.ID] = c
	}
	f.mu.Unlock()
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusCreated, f.commentJSON(c))
}

func checkJSON(c *CheckRun) map[string]any {
	return map[string]any{"id": c.ID, "name": c.Name, "head_sha": c.HeadSHA, "status": c.Status, "conclusion": c.Conclusion,
		"external_id": c.ExternalID, "output": map[string]any{"title": c.Title, "summary": c.Summary}}
}

func (f *Fake) createCheckRun(w http.ResponseWriter, r *http.Request) {
	if !f.writeCall(w, r) {
		return
	}
	var in struct {
		Name       string `json:"name"`
		HeadSHA    string `json:"head_sha"`
		ExternalID string `json:"external_id"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Output     *struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		} `json:"output"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	f.nextID++
	c := &CheckRun{ID: f.nextID, Repo: r.PathValue("owner") + "/" + r.PathValue("repo"), Name: in.Name, HeadSHA: in.HeadSHA, Status: in.Status,
		Conclusion: in.Conclusion, ExternalID: in.ExternalID}
	if c.Status == "" {
		c.Status = "queued"
	}
	if c.Conclusion != "" {
		c.Status = "completed"
	}
	if in.Output != nil {
		c.Title, c.Summary = in.Output.Title, in.Output.Summary
	}
	f.checkRuns[c.ID] = c
	f.mu.Unlock()
	writeJSON(w, http.StatusCreated, checkJSON(c))
}

func (f *Fake) listCheckRuns(w http.ResponseWriter, r *http.Request) {
	if !f.instCall(w, r) {
		return
	}
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	name := r.URL.Query().Get("check_name")
	runs := []map[string]any{}
	for _, c := range f.CheckRuns() {
		if c.Repo == repo && c.HeadSHA == r.PathValue("ref") && (name == "" || c.Name == name) {
			runs = append(runs, checkJSON(&c))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(runs), "check_runs": runs})
}

// reviewThreads answers the query of the threads of a pull request: one
// thread for each top review comment.
func (f *Fake) reviewThreads(w http.ResponseWriter, vars map[string]any) {
	repo := fmt.Sprint(vars["owner"]) + "/" + fmt.Sprint(vars["name"])
	number, _ := vars["number"].(float64)
	nodes := []map[string]any{}
	for _, c := range f.ReviewComments(repo) {
		if c.Number == int(number) && c.InReplyTo == 0 {
			nodes = append(nodes, map[string]any{"id": ThreadID(c.ID), "isResolved": c.Resolved,
				"comments": map[string]any{"nodes": []map[string]any{{"databaseId": c.ID}}}})
		}
	}
	threads := map[string]any{"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""}, "nodes": nodes}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": threads}}}})
}

// graphql handles the review thread query and mutations that Sayso uses.
func (f *Fake) graphql(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if strings.Contains(in.Query, "reviewThreads") {
		if f.instCall(w, r) {
			f.reviewThreads(w, in.Variables)
		}
		return
	}
	if !f.writeCall(w, r) {
		return
	}
	thread, _ := in.Variables["threadId"].(string)
	resolve := strings.Contains(in.Query, "resolveReviewThread") && !strings.Contains(in.Query, "unresolveReviewThread")
	if !strings.Contains(in.Query, "resolveReviewThread") {
		writeJSON(w, http.StatusOK, map[string]any{"errors": []map[string]string{{"message": "unsupported query"}}})
		return
	}
	f.mu.Lock()
	found := false
	for _, c := range f.comments {
		if ThreadID(c.ID) == thread {
			c.Resolved, found = resolve, true
		}
	}
	f.mu.Unlock()
	if !found {
		writeJSON(w, http.StatusOK, map[string]any{"errors": []map[string]string{{"message": "Could not resolve to a node"}}})
		return
	}
	key := "resolveReviewThread"
	if !resolve {
		key = "unresolveReviewThread"
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{key: map[string]any{"thread": map[string]any{"id": thread, "isResolved": resolve}}}})
}

// ThreadID is the review thread node ID of the thread that a top comment starts.
func ThreadID(commentID int64) string { return fmt.Sprintf("PRRT_%d", commentID) }
