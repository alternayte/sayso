package github

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Repo is one repository of the acme organization.
type Repo struct {
	ID            int64
	FullName      string
	Private       bool
	DefaultBranch string
	Pulls         map[int]*Pull
	nextPR        int
	dir           string
}

// Pull is one pull request.
type Pull struct {
	Number  int
	NodeID  string
	Author  string
	Draft   bool
	State   string // open or closed
	Merged  bool
	HeadSHA string
	HeadRef string
	BaseSHA string
	BaseRef string
	Labels  []string
	Title   string
	Body    string
}

// Comment is a pull request review comment.
type Comment struct {
	ID        int64
	Repo      string
	Number    int
	Body      string
	Author    string
	InReplyTo int64
	Path      string
	Line      int
	StartLine int
	CommitID  string
	ReviewID  int64
	// Resolved is the state of the thread that this top comment starts.
	Resolved bool
}

// CheckRun is a check run that a client created or changed.
type CheckRun struct {
	ID         int64
	Repo       string
	Name       string
	HeadSHA    string
	Status     string
	Conclusion string
	Title      string
	Summary    string
	ExternalID string
}

// seedRepos creates the git repositories of SDD §22 from the fixtures.
func (f *Fake) seedRepos() {
	dir, err := os.MkdirTemp("", "sayso-fake-git-")
	if err != nil {
		panic(err)
	}
	f.gitRoot = dir
	for i, name := range []string{"payments", "web", "shared-go"} {
		full := f.Org + "/" + name
		r := &Repo{ID: int64(5001 + i), FullName: full, Private: true, DefaultBranch: "main", Pulls: map[int]*Pull{}, nextPR: 1}
		if err := f.initRepo(r, name); err != nil {
			panic("fake GitHub needs git: " + err.Error())
		}
		f.repos[full] = r
	}
}

// Close removes the git repositories.
func (f *Fake) Close() error { return os.RemoveAll(f.gitRoot) }

// MainSHA returns the head of main of a repository.
func (f *Fake) MainSHA(repo string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha, _ := f.git(f.repos[repo].dir, "rev-parse", "main")
	return sha
}

// CommitMain adds a commit to main, for changes of the base branch.
func (f *Fake) CommitMain(repo string, files map[string]string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[repo]
	sha, err := f.commit(r, "main", "main", files, "Change main")
	if err != nil {
		panic(err)
	}
	return sha
}

// Repo returns a repository by full name.
func (f *Fake) Repo(full string) *Repo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repos[full]
}

func (f *Fake) repoJSON(r *Repo) map[string]any {
	return map[string]any{"id": r.ID, "full_name": r.FullName, "name": strings.TrimPrefix(r.FullName, f.Org+"/"), "private": r.Private, "default_branch": r.DefaultBranch}
}

// Install sends the installation created event with the named repositories.
func (f *Fake) Install(names ...string) (string, int) {
	f.mu.Lock()
	list := f.repoList(names)
	f.mu.Unlock()
	return f.Send("installation", map[string]any{"action": "created", "repositories": list, "sender": map[string]any{"login": "octo-admin"}})
}

// ChangeRepositories sends installation_repositories added or removed.
func (f *Fake) ChangeRepositories(action string, names ...string) (string, int) {
	f.mu.Lock()
	list := f.repoList(names)
	f.mu.Unlock()
	key := "repositories_added"
	if action == "removed" {
		key = "repositories_removed"
	}
	return f.Send("installation_repositories", map[string]any{"action": action, key: list, "sender": map[string]any{"login": "octo-admin"}})
}

func (f *Fake) repoList(names []string) []map[string]any {
	var out []map[string]any
	for _, n := range names {
		if r := f.repos[n]; r != nil {
			out = append(out, f.repoJSON(r))
		}
	}
	return out
}

// PullOptions describe a new pull request.
type PullOptions struct {
	Author string
	Draft  bool
	Head   string
	Title  string
	Body   string
	// Files of the head commit; an empty content deletes a file.
	Files map[string]string
}

// OpenPull creates a branch with one commit and a pull request for it,
// without sending an event.
func (f *Fake) OpenPull(repo string, o PullOptions) *Pull {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[repo]
	n := r.nextPR
	r.nextPR++
	ref := o.Head
	if ref == "" {
		ref = fmt.Sprintf("feature-%d", n)
	}
	files := o.Files
	if files == nil {
		files = map[string]string{fmt.Sprintf("CHANGES-%d.md", n): fmt.Sprintf("Change %d\n", n)}
	}
	title := o.Title
	if title == "" {
		title = fmt.Sprintf("Change %d", n)
	}
	base, err := f.git(r.dir, "rev-parse", "main")
	if err != nil {
		panic(err)
	}
	head, err := f.commit(r, ref, base, files, title)
	if err != nil {
		panic(err)
	}
	p := &Pull{Number: n, NodeID: fmt.Sprintf("PR_%d_%d", r.ID, n), Author: o.Author, Draft: o.Draft, State: "open", HeadSHA: head, HeadRef: ref, BaseSHA: base, BaseRef: "main",
		Title: title, Body: o.Body}
	r.Pulls[n] = p
	return p
}

// PullEvent sends a pull_request event for a pull request.
func (f *Fake) PullEvent(repo string, number int, action string, extra map[string]any) (string, int) {
	f.mu.Lock()
	r := f.repos[repo]
	p := r.Pulls[number]
	payload := map[string]any{"action": action, "number": number, "pull_request": f.pullJSON(r, p), "repository": f.repoJSON(r), "sender": map[string]any{"login": p.Author}}
	f.mu.Unlock()
	for k, v := range extra {
		payload[k] = v
	}
	return f.Send("pull_request", payload)
}

// Push adds a commit to the head of a pull request and sends synchronize.
// A forced push replaces history: the new commit starts at the base.
func (f *Fake) Push(repo string, number int, forced bool) (string, int) {
	return f.PushFiles(repo, number, forced, nil)
}

// PushFiles pushes a commit with files to a pull request and sends synchronize.
func (f *Fake) PushFiles(repo string, number int, forced bool, files map[string]string) (string, int) {
	f.mu.Lock()
	r := f.repos[repo]
	p := r.Pulls[number]
	before := p.HeadSHA
	from := before
	if forced {
		from = p.BaseSHA
	}
	if files == nil {
		files = map[string]string{fmt.Sprintf("CHANGES-%d.md", number): fmt.Sprintf("Change %d at %s\n", number, before[:7])}
	}
	after, err := f.commit(r, p.HeadRef, from, files, "Update")
	if err != nil {
		f.mu.Unlock()
		panic(err)
	}
	p.HeadSHA = after
	f.mu.Unlock()
	return f.PullEvent(repo, number, "synchronize", map[string]any{"before": before, "after": after})
}

// SetPull changes a pull request without sending an event.
func (f *Fake) SetPull(repo string, number int, change func(*Pull)) {
	f.mu.Lock()
	change(f.repos[repo].Pulls[number])
	f.mu.Unlock()
}

// AddComment stores a review comment and returns its ID.
func (f *Fake) AddComment(repo, author, body string, inReplyTo int64) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.comments[f.nextID] = &Comment{ID: f.nextID, Repo: repo, Body: body, Author: author, InReplyTo: inReplyTo}
	return f.nextID
}

// CheckRuns returns the check runs, by ID order.
func (f *Fake) CheckRuns() []CheckRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []CheckRun
	for _, c := range f.checkRuns {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AddCheckRun stores a check run and returns its ID.
func (f *Fake) AddCheckRun(repo, name, head string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.checkRuns[f.nextID] = &CheckRun{ID: f.nextID, Repo: repo, Name: name, HeadSHA: head, Status: "queued"}
	return f.nextID
}

func (f *Fake) pullJSON(r *Repo, p *Pull) map[string]any {
	labels := []map[string]any{}
	for _, l := range p.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	state := p.State
	return map[string]any{
		"number": p.Number, "node_id": p.NodeID, "draft": p.Draft, "state": state, "merged": p.Merged, "title": p.Title, "body": p.Body,
		"user":   map[string]any{"login": p.Author},
		"head":   map[string]any{"sha": p.HeadSHA, "ref": p.HeadRef, "repo": map[string]any{"id": r.ID, "full_name": r.FullName}},
		"base":   map[string]any{"sha": p.BaseSHA, "ref": p.BaseRef, "repo": map[string]any{"id": r.ID, "full_name": r.FullName}},
		"labels": labels,
	}
}

type rateState struct {
	limit, remaining int
	reset            time.Time
	retryAfter       int // seconds, sent on the next installation call only
}

// SetRateLimit makes installation calls report this primary limit.
func (f *Fake) SetRateLimit(limit, remaining int, reset time.Time) {
	f.mu.Lock()
	f.rate.limit, f.rate.remaining, f.rate.reset = limit, remaining, reset
	f.mu.Unlock()
}

// SetSecondaryLimit makes the next installation call fail with 403 and a
// retry-after header.
func (f *Fake) SetSecondaryLimit(seconds int) {
	f.mu.Lock()
	f.rate.retryAfter = seconds
	f.mu.Unlock()
}

// instCall checks an installation token and writes the rate limit headers.
// It returns false when it already answered.
func (f *Fake) instCall(w http.ResponseWriter, r *http.Request) bool {
	f.mu.Lock()
	_, ok := f.instToks[f.bearer(r)]
	rs := f.rate
	f.rate.retryAfter = 0
	f.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return false
	}
	if rs.limit > 0 {
		w.Header().Set("X-Ratelimit-Limit", strconv.Itoa(rs.limit))
		w.Header().Set("X-Ratelimit-Remaining", strconv.Itoa(rs.remaining))
		w.Header().Set("X-Ratelimit-Reset", strconv.FormatInt(rs.reset.Unix(), 10))
	}
	if rs.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(rs.retryAfter))
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "You have exceeded a secondary rate limit."})
		return false
	}
	return true
}

func (f *Fake) repoRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls", f.listPulls)
	mux.HandleFunc("GET /repos/{owner}/{repo}/compare/{basehead}", f.compare)
	mux.HandleFunc("GET /repos/{owner}/{repo}/contents/{path...}", f.contents)
	mux.HandleFunc("GET /{owner}/{repo}/info/refs", f.gitHTTP)
	mux.HandleFunc("POST /{owner}/{repo}/git-upload-pack", f.gitHTTP)
	mux.HandleFunc("POST /{owner}/{repo}/git-receive-pack", f.gitHTTP)
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/comments/{id}", f.getComment)
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/check-runs/{id}", f.patchCheckRun)
	f.publishRoutes(mux)
}

func (f *Fake) repoOf(r *http.Request) *Repo {
	return f.repos[r.PathValue("owner")+"/"+r.PathValue("repo")]
}

func (f *Fake) listPulls(w http.ResponseWriter, r *http.Request) {
	if !f.instCall(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	repo := f.repoOf(r)
	if repo == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	out := []map[string]any{}
	nums := make([]int, 0, len(repo.Pulls))
	for n := range repo.Pulls {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	for _, n := range nums {
		if p := repo.Pulls[n]; p.State == "open" {
			out = append(out, f.pullJSON(repo, p))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) compare(w http.ResponseWriter, r *http.Request) {
	if !f.instCall(w, r) {
		return
	}
	base, head, ok := strings.Cut(r.PathValue("basehead"), "...")
	f.mu.Lock()
	repo := f.repoOf(r)
	status := "diverged"
	if repo != nil && ok {
		switch anc, known := f.isAncestor(repo, base, head); {
		case !known:
			f.mu.Unlock()
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		case base == head:
			status = "identical"
		case anc:
			status = "ahead"
		}
	}
	f.mu.Unlock()
	if repo == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

func (f *Fake) getComment(w http.ResponseWriter, r *http.Request) {
	if !f.instCall(w, r) {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f.mu.Lock()
	c := f.comments[id]
	f.mu.Unlock()
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": c.ID, "body": c.Body, "user": map[string]any{"login": c.Author}, "in_reply_to_id": c.InReplyTo})
}

func (f *Fake) patchCheckRun(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Output     *struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		} `json:"output"`
	}
	if !f.writeCall(w, r) {
		return
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	c := f.checkRuns[id]
	if c != nil {
		if in.Status != "" {
			c.Status = in.Status
		}
		if in.Conclusion != "" {
			c.Conclusion, c.Status = in.Conclusion, "completed"
		}
		if in.Output != nil {
			c.Title, c.Summary = in.Output.Title, in.Output.Summary
		}
	}
	f.mu.Unlock()
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (f *Fake) contents(w http.ResponseWriter, r *http.Request) {
	if !f.instCall(w, r) {
		return
	}
	f.mu.Lock()
	repo := f.repoOf(r)
	var out string
	var err error
	if repo != nil {
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = "main"
		}
		out, err = f.git(repo.dir, "show", ref+":"+r.PathValue("path"))
	}
	f.mu.Unlock()
	if repo == nil || err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"type": "file", "encoding": "base64", "path": r.PathValue("path"),
		"content": base64.StdEncoding.EncodeToString([]byte(out + "\n"))})
}
