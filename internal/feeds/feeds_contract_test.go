//go:build contract

package feeds_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alternayte/sayso/internal/app/apptest"
	fakefeeds "github.com/alternayte/sayso/internal/fakes/feeds"
	"github.com/alternayte/sayso/internal/feeds"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/secrets"
)

const feedCred = "Basic " + "ZmVlZDpzM2NyM3QtZmVlZC10b2tlbg==" // feed:s3cr3t-feed-token

type env struct {
	*apptest.Env
	admin *http.Client
	fake  *fakefeeds.Fake
	url   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := apptest.New(t, apptest.Options{BootstrapAdmins: []string{"octo-admin"}})
	admin, _ := e.SignIn(t, "octo-admin")
	ff := fakefeeds.New(feedCred)
	srv := httptest.NewServer(ff.Handler())
	t.Cleanup(srv.Close)
	e.GitHub.Install("acme/payments", "acme/web", "acme/shared-go")
	return &env{Env: e, admin: admin, fake: ff, url: srv.URL}
}

func (e *env) feed(t *testing.T, in feeds.FeedInput) feeds.FeedBody {
	t.Helper()
	var out feeds.FeedBody
	if code := apptest.JSON(t, e.admin, http.MethodPut, e.Base+"/api/v1/feeds/"+in.Name, in, &out); code != 200 {
		t.Fatalf("put feed %s: %d", in.Name, code)
	}
	return out
}

func (e *env) repoID(t *testing.T, full string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.Deps.Pool.QueryRow(context.Background(), `SELECT id FROM repositories WHERE full_name = $1`, full).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// run creates an active run of a repository whose spec has the feeds.
func (e *env) run(t *testing.T, repo string, names ...string) string {
	t.Helper()
	sf, err := e.F.Feeds.SpecFeeds(context.Background(), repo, names)
	if err != nil {
		t.Fatal(err)
	}
	id := e.repoID(t, repo)
	_, tok, err := runner.Create(context.Background(), e.Deps.Pool, runner.NewRun{Kind: "round", RepositoryID: &id, Spec: runner.Spec{Feeds: sf}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func get(t *testing.T, method, url, tok string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x:"+tok)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func secret() *secrets.Input { return &secrets.Input{Value: "s3cr3t-feed-token"} }

func TestSCN_PKG_001_FeedTestAndRepositoryScope(t *testing.T) {
	e := newEnv(t)
	e.feed(t, feeds.FeedInput{Name: "acme-npm", Kind: "npm", UpstreamURL: e.url + "/npm/", AuthStyle: "basic", Username: "feed", Secret: secret(), Allowed: []string{"acme/web"}})
	e.feed(t, feeds.FeedInput{Name: "acme-nuget", Kind: "nuget", UpstreamURL: e.url + "/nuget/v3/index.json", AuthStyle: "basic", Username: "feed", Secret: secret()})
	for _, n := range []string{"acme-npm", "acme-nuget"} {
		var res struct {
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		}
		apptest.JSON(t, e.admin, http.MethodPost, e.Base+"/api/v1/feeds/"+n+"/test", nil, &res)
		if !res.OK {
			t.Fatalf("test %s: %s", n, res.Detail)
		}
	}
	for _, r := range e.fake.Requests() {
		if r.Auth != feedCred {
			t.Fatalf("the feed test sent %q", r.Auth)
		}
	}
	web, err := e.F.Feeds.SpecFeeds(context.Background(), "acme/web", []string{"acme-npm", "acme-nuget"})
	if err != nil || len(web) != 2 {
		t.Fatalf("acme/web feeds %+v %v", web, err)
	}
	pay, _ := e.F.Feeds.SpecFeeds(context.Background(), "acme/payments", []string{"acme-npm", "acme-nuget"})
	if len(pay) != 1 || pay[0].Name != "acme-nuget" || pay[0].Credential != "" {
		t.Fatalf("acme/payments feeds %+v", pay)
	}
}

func TestSCN_PKG_002_ProxyRewritesAndReadsOnly(t *testing.T) {
	e := newEnv(t)
	e.feed(t, feeds.FeedInput{Name: "acme-npm", Kind: "npm", UpstreamURL: e.url + "/npm/", AuthStyle: "basic", Username: "feed", Secret: secret()})
	e.feed(t, feeds.FeedInput{Name: "acme-nuget", Kind: "nuget", UpstreamURL: e.url + "/nuget/", AuthStyle: "basic", Username: "feed", Secret: secret()})
	e.feed(t, feeds.FeedInput{Name: "acme-git", Kind: "github", Repos: []string{"acme/shared-go"}})
	tok := e.run(t, "acme/web", "acme-npm", "acme-nuget", "acme-git")
	proxy := e.Deps.Config.RunnerAPIURL + "/pkg/"

	code, meta := get(t, http.MethodGet, e.Base+"/pkg/acme-npm/@acme%2fmoney", tok)
	if code != 200 || strings.Contains(meta, e.url) || !strings.Contains(meta, proxy+"acme-npm/@acme/money/-/money-1.0.0.tgz") {
		t.Fatalf("npm metadata %d: %s", code, meta)
	}
	code, idx := get(t, http.MethodGet, e.Base+"/pkg/acme-nuget/v3/index.json", tok)
	if code != 200 || strings.Contains(idx, e.url) || !strings.Contains(idx, proxy+"acme-nuget/v3/flatcontainer/") {
		t.Fatalf("nuget index %d: %s", code, idx)
	}
	if code, body := get(t, http.MethodGet, e.Base+"/pkg/acme-npm/@acme/money/-/money-1.0.0.tgz", tok); code != 200 || len(body) < 50 {
		t.Fatalf("tarball %d", code)
	}
	for _, r := range e.fake.Requests() {
		if r.Auth != feedCred || strings.Contains(r.Auth, tok) {
			t.Fatalf("the feed got %q", r.Auth)
		}
	}
	// Publish and push are refused.
	if code, _ := get(t, http.MethodPut, e.Base+"/pkg/acme-npm/@acme%2fmoney", tok); code != http.StatusMethodNotAllowed {
		t.Fatalf("npm publish: %d", code)
	}
	if code, _ := get(t, http.MethodPost, e.Base+"/pkg/acme-git/acme/shared-go.git/git-receive-pack", tok); code != http.StatusMethodNotAllowed {
		t.Fatalf("git receive-pack: %d", code)
	}
	if code, _ := get(t, http.MethodGet, e.Base+"/pkg/acme-git/acme/shared-go.git/info/refs?service=git-receive-pack", tok); code != http.StatusMethodNotAllowed {
		t.Fatalf("git receive-pack refs: %d", code)
	}
	// Git reads work for listed repositories only.
	if code, body := get(t, http.MethodGet, e.Base+"/pkg/acme-git/acme/shared-go.git/info/refs?service=git-upload-pack", tok); code != 200 || !strings.Contains(body, "refs/heads/main") {
		t.Fatalf("git refs %d: %.200s", code, body)
	}
	if code, _ := get(t, http.MethodGet, e.Base+"/pkg/acme-git/acme/payments.git/info/refs?service=git-upload-pack", tok); code != http.StatusForbidden {
		t.Fatalf("unlisted repository: %d", code)
	}
	if code, _ := get(t, http.MethodGet, e.Base+"/pkg/acme-npm/@acme%2fmoney", "srt_unknown"); code != http.StatusUnauthorized {
		t.Fatalf("unknown token: %d", code)
	}
}

func TestSCN_PKG_006_CacheVerifiesAndEvicts(t *testing.T) {
	e := newEnv(t)
	e.feed(t, feeds.FeedInput{Name: "acme-npm", Kind: "npm", UpstreamURL: e.url + "/npm/", AuthStyle: "basic", Username: "feed", Secret: secret()})
	tarball := e.Base + "/pkg/acme-npm/@acme/money/-/money-1.0.0.tgz"
	meta := e.Base + "/pkg/acme-npm/@acme%2fmoney"
	for i := 0; i < 2; i++ {
		tok := e.run(t, "acme/web", "acme-npm")
		if code, _ := get(t, http.MethodGet, meta, tok); code != 200 {
			t.Fatalf("metadata %d", code)
		}
		if code, _ := get(t, http.MethodGet, tarball, tok); code != 200 {
			t.Fatalf("tarball %d", code)
		}
	}
	if n, m := e.fake.Count(".tgz"), e.fake.Count("/npm/@acme/money"); n != 1 || m != 2 {
		t.Fatalf("feed requests: %d tarballs, %d metadata", n, m)
	}
	// A changed cached file is refused, deleted and fetched again.
	e.SQL(t, `UPDATE blobs SET data = 'changed' WHERE key LIKE 'pkg/acme-npm/%'`)
	tok := e.run(t, "acme/web", "acme-npm")
	code, body := get(t, http.MethodGet, tarball, tok)
	if code != 200 || body == "changed" || e.fake.Count(".tgz") != 2 {
		t.Fatalf("changed file: %d, %d tarball fetches", code, e.fake.Count(".tgz"))
	}
	// Over the size limit, the least recently used file goes.
	e.SQL(t, `UPDATE package_feeds SET cache_max_bytes = 1`)
	e.SQL(t, `INSERT INTO pkg_cache (feed, path, store_id, key, size, sha256, last_access_at) SELECT 'acme-npm', '/old.tgz', store_id, 'pkg/acme-npm/old', 10, 'x', now() - interval '1 day' FROM pkg_cache LIMIT 1`)
	e.SQL(t, `DELETE FROM pkg_cache WHERE path <> '/old.tgz'`)
	e.fake.Reset()
	get(t, http.MethodGet, tarball, tok)
	if n := e.Count(t, `SELECT count(*) FROM pkg_cache WHERE path = '/old.tgz'`); n != 0 {
		t.Fatal("the least recently used file was not evicted")
	}
	// The run summary counts the requests and the bytes.
	id := runIDOf(t, e, tok)
	if err := e.F.Feeds.Summary(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if n := e.Count(t, `SELECT count(*) FROM run_events WHERE run_id = $1 AND type = 'pkg_summary' AND preview LIKE 'acme-npm: 2 requests%'`, id); n != 1 {
		t.Fatalf("pkg_summary events: %d", n)
	}
}

func runIDOf(t *testing.T, e *env, tok string) uuid.UUID {
	t.Helper()
	r, err := runner.Active(context.Background(), e.Deps.Pool, tok, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}
