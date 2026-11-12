// Package github is a fake of the GitHub web and REST API for tests and
// the Compose "fakes" profile. It holds the synthetic acme organization and
// records every request.
package github

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// User is a GitHub account.
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

// Request is one recorded call.
type Request struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Body   string
	At     time.Time
}

// Fake is a running fake GitHub. Use Handler with an HTTP server.
type Fake struct {
	mu        sync.Mutex
	Org       string
	users     map[string]User
	members   map[string]bool
	teams     map[string]map[string]bool // team slug -> logins
	codes     map[string]string          // OAuth code -> login
	userToks  map[string]string          // user token -> login
	instToks  map[string]TokenGrant
	requests  []Request
	nextLogin string
	ClientID  string
	Secret    string
	InstallID int64

	// App and webhooks (app.go).
	AppID         int64
	AppSlug       string
	HookURL       string
	WebhookSecret string
	manifests     map[string]manifest
	deliveries    []*Delivery
	nextDelivery  int64
	httpc         *http.Client
	gitRoot       string

	// Repositories, pull requests and rate limits (repos.go).
	repos     map[string]*Repo
	comments  map[int64]*Comment
	checkRuns map[int64]*CheckRun
	nextID    int64
	rate      rateState
}

// New returns the fake with the acme organization of SDD §22: octo-admin
// (owner, team platform), dev-alice (write), dev-bob (read) and mallory
// (not a member).
func New() *Fake {
	f := &Fake{
		Org:       "acme",
		users:     map[string]User{},
		members:   map[string]bool{},
		teams:     map[string]map[string]bool{"platform": {}},
		codes:     map[string]string{},
		userToks:  map[string]string{},
		instToks:  map[string]TokenGrant{},
		ClientID:  "Iv1.acmeclient",
		Secret:    "acme-client-secret",
		InstallID: 4242,
		AppID:     77,
		AppSlug:   "sayso-acme",
		manifests: map[string]manifest{},
		httpc:     &http.Client{Timeout: 30 * time.Second},
		repos:     map[string]*Repo{},
		comments:  map[int64]*Comment{},
		checkRuns: map[int64]*CheckRun{},
		nextID:    90000,
	}
	f.seedRepos()
	for i, u := range []struct {
		login, name string
		member      bool
	}{
		{"octo-admin", "Octo Admin", true}, {"dev-alice", "Dev Alice", true},
		{"dev-bob", "Dev Bob", true}, {"mallory", "Mallory", false},
	} {
		f.users[u.login] = User{ID: int64(1001 + i), Login: u.login, Name: u.name}
		f.members[u.login] = u.member
	}
	f.teams["platform"]["octo-admin"] = true
	return f
}

// SignInAs sets the account that the next OAuth authorize request uses.
func (f *Fake) SignInAs(login string) {
	f.mu.Lock()
	f.nextLogin = login
	f.mu.Unlock()
}

// SetTeamMember adds or removes a login from a team.
func (f *Fake) SetTeamMember(team, login string, member bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.teams[team] == nil {
		f.teams[team] = map[string]bool{}
	}
	if member {
		f.teams[team][login] = true
	} else {
		delete(f.teams[team], login)
	}
}

// SetOrgMember adds or removes a login from the organization.
func (f *Fake) SetOrgMember(login string, member bool) {
	f.mu.Lock()
	f.members[login] = member
	f.mu.Unlock()
}

// Requests returns a copy of the recorded requests.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// Handler serves the web and API routes on one host. Point both the web
// URL and the API URL settings at it.
func (f *Fake) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/oauth/authorize", f.authorize)
	mux.HandleFunc("POST /login/oauth/access_token", f.accessToken)
	mux.HandleFunc("GET /user", f.user)
	mux.HandleFunc("GET /orgs/{org}/members/{login}", f.orgMember)
	mux.HandleFunc("GET /orgs/{org}/installation", f.orgInstallation)
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", f.installationToken)
	mux.HandleFunc("GET /orgs/{org}/teams/{team}/memberships/{login}", f.teamMembership)
	mux.HandleFunc("GET /orgs/{org}/teams", f.listTeams)
	f.appRoutes(mux)
	f.repoRoutes(mux)
	return f.record(mux)
}

func (f *Fake) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		r.Body = io.NopCloser(bytes.NewReader(raw))
		body := string(raw)
		if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
			_ = r.ParseForm()
			body = r.PostForm.Encode()
		}
		f.mu.Lock()
		f.requests = append(f.requests, Request{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Auth: r.Header.Get("Authorization"), Body: body, At: time.Now(),
		})
		f.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func token(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (f *Fake) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != f.ClientID {
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	login := f.nextLogin
	code := token("code_")
	f.codes[code] = login
	f.mu.Unlock()
	if login == "" {
		http.Error(w, "no account: call SignInAs", http.StatusUnauthorized)
		return
	}
	u, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	v := u.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (f *Fake) accessToken(w http.ResponseWriter, r *http.Request) {
	if r.PostForm.Get("client_id") != f.ClientID || r.PostForm.Get("client_secret") != f.Secret {
		writeJSON(w, http.StatusOK, map[string]string{"error": "incorrect_client_credentials"})
		return
	}
	f.mu.Lock()
	login, ok := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code"))
	tok := token("ghu_")
	if ok {
		f.userToks[tok] = login
	}
	f.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"error": "bad_verification_code"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"access_token": tok, "token_type": "bearer"})
}

func (f *Fake) bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	for _, p := range []string{"Bearer ", "token "} {
		if strings.HasPrefix(h, p) {
			return strings.TrimPrefix(h, p)
		}
	}
	return ""
}

func (f *Fake) user(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	login, ok := f.userToks[f.bearer(r)]
	u := f.users[login]
	f.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (f *Fake) authorized(r *http.Request) bool {
	t := f.bearer(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	_, user := f.userToks[t]
	_, inst := f.instToks[t]
	return user || inst
}

func (f *Fake) orgMember(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	f.mu.Lock()
	ok := r.PathValue("org") == f.Org && f.members[r.PathValue("login")]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *Fake) orgInstallation(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || r.PathValue("org") != f.Org {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": f.InstallID, "account": map[string]string{"login": f.Org}})
}

func (f *Fake) installationToken(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if id != f.InstallID || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	var grant TokenGrant
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&grant); err != nil && err != io.EOF {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "bad body"})
			return
		}
	}
	tok := token("ghs_")
	grant.At = time.Now()
	f.mu.Lock()
	f.instToks[tok] = grant
	f.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"token": tok, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
}

func (f *Fake) teamMembership(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	f.mu.Lock()
	ok := r.PathValue("org") == f.Org && f.teams[r.PathValue("team")][r.PathValue("login")]
	f.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": "active", "role": "member"})
}

func (f *Fake) listTeams(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	f.mu.Lock()
	var out []map[string]string
	for slug := range f.teams {
		out = append(out, map[string]string{"slug": slug, "name": slug})
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
