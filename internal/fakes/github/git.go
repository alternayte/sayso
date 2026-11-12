package github

import (
	"embed"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed all:_fixtures
var fixtures embed.FS

// gitEnv makes commits of the fake repeatable.
var gitEnv = []string{
	"GIT_AUTHOR_NAME=Acme Bot", "GIT_AUTHOR_EMAIL=bot@acme.test", "GIT_COMMITTER_NAME=Acme Bot", "GIT_COMMITTER_EMAIL=bot@acme.test",
	"GIT_AUTHOR_DATE=2026-01-05T09:00:00Z", "GIT_COMMITTER_DATE=2026-01-05T09:00:00Z", "GIT_CONFIG_NOSYSTEM=1", "HOME=/nonexistent",
}

func (f *Fake) git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// initRepo creates the git repository of a fixture with one commit on main.
func (f *Fake) initRepo(r *Repo, fixture string) error {
	dir := filepath.Join(f.gitRoot, r.FullName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	sub, err := fs.Sub(fixtures, "_fixtures/"+fixture)
	if err != nil {
		return err
	}
	if err := os.CopyFS(dir, sub); err != nil {
		return err
	}
	// A go.mod in the fixtures would make them a module of their own, which
	// go:embed leaves out, so they carry it as go.mod.txt.
	if _, err := os.Stat(filepath.Join(dir, "go.mod.txt")); err == nil {
		if err := os.Rename(filepath.Join(dir, "go.mod.txt"), filepath.Join(dir, "go.mod")); err != nil {
			return err
		}
	}
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=main"},
		{"config", "uploadpack.allowAnySHA1InWant", "true"},
		{"config", "uploadpack.allowFilter", "true"},
		{"config", "http.receivepack", "false"},
		{"add", "--all"},
		{"commit", "--quiet", "-m", "Initial commit"},
	} {
		if _, err := f.git(dir, args...); err != nil {
			return err
		}
	}
	r.dir = dir
	return nil
}

// commit writes files on a branch that starts at from and returns the new SHA.
// An empty content deletes the file.
func (f *Fake) commit(r *Repo, branch, from string, files map[string]string, message string) (string, error) {
	if _, err := f.git(r.dir, "checkout", "--quiet", "-B", branch, from); err != nil {
		return "", err
	}
	for p, content := range files {
		full := filepath.Join(r.dir, p)
		if content == "" {
			if _, err := f.git(r.dir, "rm", "--quiet", "-f", "--", p); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return "", err
		}
		if _, err := f.git(r.dir, "add", "--", p); err != nil {
			return "", err
		}
	}
	if _, err := f.git(r.dir, "commit", "--quiet", "--allow-empty", "-m", message); err != nil {
		return "", err
	}
	sha, err := f.git(r.dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	_, err = f.git(r.dir, "checkout", "--quiet", "main")
	return sha, err
}

func (f *Fake) isAncestor(r *Repo, base, head string) (bool, bool) {
	if _, err := f.git(r.dir, "cat-file", "-e", head+"^{commit}"); err != nil {
		return false, false
	}
	_, err := f.git(r.dir, "merge-base", "--is-ancestor", base, head)
	return err == nil, true
}

// gitHTTP serves git smart HTTP read calls for the fixture repositories with
// an installation token that may read the repository.
func (f *Fake) gitHTTP(w http.ResponseWriter, r *http.Request) {
	owner, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	name, _, _ := strings.Cut(rest, ".git/")
	full := owner + "/" + name
	f.mu.Lock()
	repo := f.repos[full]
	f.mu.Unlock()
	if repo == nil || repo.dir == "" {
		http.NotFound(w, r)
		return
	}
	if strings.Contains(r.URL.Path, "git-receive-pack") || r.URL.Query().Get("service") == "git-receive-pack" {
		http.Error(w, "push is not allowed", http.StatusForbidden)
		return
	}
	if !f.canRead(r, name) {
		w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}
	backend, err := exec.LookPath("git")
	if err != nil {
		http.Error(w, "git is not installed", http.StatusInternalServerError)
		return
	}
	h := &cgi.Handler{
		Path: backend,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + f.gitRoot, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1"},
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + full + "/.git/" + strings.SplitN(r.URL.Path, ".git/", 2)[1]
	h.ServeHTTP(w, r2)
}

// canRead checks the Basic password against the installation tokens.
func (f *Fake) canRead(r *http.Request, repo string) bool {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Basic ") {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "Basic "))
	if err != nil {
		return false
	}
	_, tok, _ := strings.Cut(string(raw), ":")
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.instToks[tok]
	if !ok {
		return false
	}
	if len(g.Repositories) == 0 {
		return true
	}
	for _, n := range g.Repositories {
		if n == repo {
			return true
		}
	}
	return false
}
