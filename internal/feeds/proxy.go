package feeds

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/runner"
)

// MaxCached is the largest file the cache keeps.
const MaxCached = 512 << 20

// runToken reads the run token from a bearer header or the password of
// basic authentication, which npm, NuGet, pip, Go and git clients use.
func runToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if strings.HasPrefix(h, "Basic ") {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "Basic "))
		if err == nil {
			_, p, _ := strings.Cut(string(raw), ":")
			return p
		}
	}
	return ""
}

// readCall reports whether a request is a read call that the proxy allows
// for a feed kind (SI-17).
func readCall(kind string, r *http.Request, path string) bool {
	if kind == KindGitHub {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/info/refs") && r.URL.Query().Get("service") == "git-upload-pack":
			return true
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/git-upload-pack"):
			return true
		}
		return false
	}
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

var (
	goModFile     = regexp.MustCompile(`/@v/[^/]+\.(zip|mod)$`)
	mavenArtifact = regexp.MustCompile(`\.(jar|pom|war|aar|module)$`)
	pypiDist      = regexp.MustCompile(`\.(whl|tar\.gz|zip|tar\.bz2)$`)
)

// Immutable reports whether a path is a package file that the cache may keep.
// Metadata is never cached, so new versions show at once (§8.11).
func Immutable(kind, path string) bool {
	switch kind {
	case KindNPM:
		return strings.Contains(path, "/-/") && strings.HasSuffix(path, ".tgz")
	case KindNuGet:
		return strings.HasSuffix(strings.ToLower(path), ".nupkg")
	case KindGoProxy:
		return goModFile.MatchString(path)
	case KindMaven:
		return mavenArtifact.MatchString(path) && !strings.Contains(path, "maven-metadata")
	case KindPyPI:
		return strings.Contains(path, "/packages/") && pypiDist.MatchString(path)
	}
	return false
}

func isText(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "json") || strings.Contains(ct, "xml") || strings.Contains(ct, "html") || strings.HasPrefix(ct, "text/")
}

func cacheKey(feed, path string) string {
	s := sha256.Sum256([]byte(path))
	return "pkg/" + feed + "/" + hex.EncodeToString(s[:])
}

func perr(w http.ResponseWriter, code int, msg string) { http.Error(w, msg, code) }

// Proxy serves /pkg/{feed}/* for agent runs (REQ-PKG-002).
func (s *Service) Proxy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "feed")
	path := "/" + chi.URLParam(r, "*")
	// A dot segment would reach another path of the feed host with the
	// feed credential (SI-17).
	if !plainPath(path) || !plainPath(r.URL.EscapedPath()) {
		perr(w, http.StatusBadRequest, "the path has a dot segment")
		return
	}
	run, err := runner.Active(ctx, s.pool, runToken(r), time.Now())
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="sayso package proxy"`)
		perr(w, http.StatusUnauthorized, "the run token is not valid")
		return
	}
	f, err := New(s.pool).Get(ctx, name)
	if err != nil {
		perr(w, http.StatusNotFound, "unknown feed")
		return
	}
	if !readCall(f.Kind, r, path) {
		perr(w, http.StatusMethodNotAllowed, "the package proxy allows read calls only")
		return
	}
	if ok, err := s.runMay(ctx, run, f); err != nil || !ok {
		perr(w, http.StatusForbidden, "this feed is not allowed for the run's repository")
		return
	}
	if f.Mode != ModeProxy {
		perr(w, http.StatusForbidden, "this feed is in inject mode")
		return
	}
	upstream, auth, err := s.upstream(ctx, f, path)
	if errors.Is(err, errRepoNotListed) {
		perr(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		perr(w, http.StatusBadGateway, s.redact(err.Error()))
		return
	}
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	cache := f.CacheEnabled && Immutable(f.Kind, path) && r.Method == http.MethodGet
	if cache && s.serveCached(ctx, w, run.ID, f, path) {
		return
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, upstream, r.Body)
	if err != nil {
		perr(w, http.StatusBadRequest, "bad request")
		return
	}
	// Keep the length: git http-backend refuses a chunked upload-pack body.
	req.ContentLength = r.ContentLength
	for _, k := range []string{"Accept", "Content-Type", "Content-Encoding", "Git-Protocol", "User-Agent", "Npm-Command", "X-Nuget-Client-Version"} {
		if v := r.Header.Get(k); v != "" {
			req.Header.Set(k, v)
		}
	}
	if auth != "" {
		req.Header.Set(f.headerName(), auth)
	}
	resp, err := s.streamClient().Do(req)
	if err != nil {
		perr(w, http.StatusBadGateway, "the feed is not reachable")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	proxyBase := s.proxyBase(f.Name)
	for _, k := range []string{"Content-Type", "Cache-Control", "Etag", "Last-Modified", "Content-Encoding"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	var n int64
	switch {
	case resp.StatusCode == http.StatusOK && isText(resp.Header.Get("Content-Type")) && resp.Header.Get("Content-Encoding") == "":
		// Rewrite the upstream prefix in documents, so tarball and resource
		// links also come through the proxy.
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			perr(w, http.StatusBadGateway, "the feed answer was cut")
			return
		}
		b = s.rewrite(f, b, proxyBase)
		w.WriteHeader(resp.StatusCode)
		m, _ := w.Write(b)
		n = int64(m)
	case cache && resp.StatusCode == http.StatusOK:
		n = s.fetchAndCache(ctx, w, resp, f, path)
	default:
		w.WriteHeader(resp.StatusCode)
		n, _ = io.Copy(w, resp.Body)
	}
	s.usage(run.ID, f.Name, n, 0)
}

func (f PackageFeed) headerName() string {
	if f.AuthStyle == AuthHeader && f.AuthHeader != "" {
		return f.AuthHeader
	}
	return "Authorization"
}

// rewrite replaces each upstream prefix, and each match URL, with the proxy URL.
func (s *Service) rewrite(f PackageFeed, b []byte, proxyBase string) []byte {
	prefixes := append([]string{strings.TrimRight(f.UpstreamUrl, "/")}, f.MatchUrls...)
	for _, p := range prefixes {
		p = strings.TrimRight(p, "/")
		if p == "" {
			continue
		}
		b = bytes.ReplaceAll(b, []byte(p), []byte(proxyBase))
		// JSON documents can escape the slashes.
		b = bytes.ReplaceAll(b, []byte(strings.ReplaceAll(p, "/", `\/`)), []byte(strings.ReplaceAll(proxyBase, "/", `\/`)))
	}
	return b
}

// serveCached serves a cached file when its SHA-256 matches the value of the
// first download (SI-20). A changed file is deleted and fetched again.
func (s *Service) serveCached(ctx context.Context, w http.ResponseWriter, runID uuid.UUID, f PackageFeed, path string) bool {
	q := New(s.pool)
	c, err := q.CacheGet(ctx, CacheGetParams{Feed: f.Name, Path: path})
	if err != nil {
		return false
	}
	store, err := s.stores.ByID(ctx, c.StoreID)
	if err != nil {
		return false
	}
	rc, err := store.Get(ctx, c.Key)
	if err != nil {
		_ = q.CacheDelete(ctx, CacheDeleteParams{Feed: f.Name, Path: path})
		return false
	}
	data, err := io.ReadAll(io.LimitReader(rc, MaxCached+1))
	_ = rc.Close()
	sum := sha256.Sum256(data)
	if err != nil || hex.EncodeToString(sum[:]) != c.Sha256 {
		s.log.Warn("a cached package file changed in the store; it is refused and fetched again", "feed", f.Name, "path", path)
		_ = store.Delete(ctx, c.Key)
		_ = q.CacheDelete(ctx, CacheDeleteParams{Feed: f.Name, Path: path})
		return false
	}
	_ = q.CacheHit(ctx, CacheHitParams{Feed: f.Name, Path: path})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Sayso-Cache", "hit")
	_, _ = w.Write(data)
	s.usage(runID, f.Name, int64(len(data)), 1)
	return true
}

// fetchAndCache streams a package file to the client and keeps it in the
// default store with its SHA-256. Then it evicts the least recently used
// files over the size limit.
func (s *Service) fetchAndCache(ctx context.Context, w http.ResponseWriter, resp *http.Response, f PackageFeed, path string) int64 {
	var buf bytes.Buffer
	w.WriteHeader(http.StatusOK)
	n, err := io.Copy(io.MultiWriter(w, &limited{&buf, MaxCached}), resp.Body)
	if err != nil || n > MaxCached {
		return n
	}
	bg := context.WithoutCancel(ctx)
	storeID, store, err := s.stores.Default(bg)
	if err != nil {
		return n
	}
	key := cacheKey(f.Name, path)
	if err := store.Put(bg, key, bytes.NewReader(buf.Bytes()), int64(buf.Len())); err != nil {
		s.log.Warn("package cache put", "error", err)
		return n
	}
	sum := sha256.Sum256(buf.Bytes())
	q := New(s.pool)
	if err := q.CachePut(bg, CachePutParams{Feed: f.Name, Path: path, StoreID: storeID, Key: key, Size: n, Sha256: hex.EncodeToString(sum[:])}); err != nil {
		return n
	}
	s.evict(bg, f)
	return n
}

type limited struct {
	b   *bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.b.Len()+len(p) <= l.max {
		l.b.Write(p)
	}
	return len(p), nil
}

// evict deletes the least recently used files while the feed is over its limit.
func (s *Service) evict(ctx context.Context, f PackageFeed) {
	q := New(s.pool)
	for {
		size, err := q.CacheSize(ctx, f.Name)
		if err != nil || size <= f.CacheMaxBytes {
			return
		}
		old, err := q.CacheOldest(ctx, CacheOldestParams{Feed: f.Name, Limit: 1})
		if err != nil || len(old) == 0 {
			return
		}
		c := old[0]
		if st, err := s.stores.ByID(ctx, c.StoreID); err == nil {
			_ = st.Delete(ctx, c.Key)
		}
		if err := q.CacheDelete(ctx, CacheDeleteParams{Feed: c.Feed, Path: c.Path}); err != nil {
			return
		}
	}
}

func (s *Service) usage(runID uuid.UUID, feed string, n, hits int64) {
	_ = New(s.pool).AddUsage(context.Background(), AddUsageParams{RunID: runID, Feed: feed, Bytes: n, CacheHits: hits})
}

// runMay reports whether the run's spec has the feed and its repository may use it.
func (s *Service) runMay(ctx context.Context, run runner.AgentRun, f PackageFeed) (bool, error) {
	var spec runner.Spec
	if err := json.Unmarshal(run.Spec, &spec); err != nil {
		return false, err
	}
	if !slices.ContainsFunc(spec.Feeds, func(x runner.SpecFeed) bool { return x.Name == f.Name }) {
		return false, nil
	}
	if f.AllowedRepositories == nil {
		return true, nil
	}
	if run.RepositoryID == nil {
		return false, nil
	}
	full, err := New(s.pool).RepositoryFullName(ctx, *run.RepositoryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(f.AllowedRepositories, func(r string) bool { return strings.EqualFold(r, full) }), nil
}

var errRepoNotListed = errors.New("the repository is not listed for this feed")

// upstream returns the upstream URL and the credential header value of a path.
func (s *Service) upstream(ctx context.Context, f PackageFeed, path string) (string, string, error) {
	if f.Kind == KindGitHub {
		repoPath, suffix := path, ""
		for _, sfx := range []string{"/info/refs", "/git-upload-pack"} {
			if strings.HasSuffix(path, sfx) {
				repoPath, suffix = strings.TrimSuffix(path, sfx), sfx
			}
		}
		full := strings.TrimSuffix(strings.Trim(repoPath, "/"), ".git")
		if !slices.ContainsFunc(f.Repos, func(r string) bool { return strings.EqualFold(r, full) }) {
			return "", "", errRepoNotListed
		}
		tok, web, err := s.gitHubToken(ctx, f.Repos)
		if err != nil {
			return "", "", err
		}
		u := strings.TrimRight(web, "/") + "/" + full + ".git" + suffix
		return u, "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok)), nil
	}
	u, err := url.JoinPath(f.UpstreamUrl, path)
	if err != nil {
		return "", "", err
	}
	if strings.HasSuffix(path, "/") && !strings.HasSuffix(u, "/") {
		u += "/"
	}
	auth, err := s.credential(ctx, f)
	return u, auth, err
}

// credential is the header value of the feed credential.
func (s *Service) credential(ctx context.Context, f PackageFeed) (string, error) {
	v, err := s.secrets.ValueOf(ctx, f.SecretID)
	if err != nil || v == "" {
		return "", err
	}
	switch f.AuthStyle {
	case AuthBasic:
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(f.Username+":"+v)), nil
	case AuthBearer:
		return "Bearer " + v, nil
	}
	return v, nil
}

func (s *Service) streamClient() *http.Client {
	c := *s.http
	c.Timeout = 0
	return &c
}

// Summary writes one pkg_summary event of a run that ended (§8.11).
func (s *Service) Summary(ctx context.Context, runID uuid.UUID) error {
	rows, err := New(s.pool).RunUsage(ctx, runID)
	if err != nil || len(rows) == 0 {
		return err
	}
	var parts []string
	var req, bytes, hits int64
	for _, u := range rows {
		parts = append(parts, fmt.Sprintf("%s: %d requests, %d bytes, %d cache hits", u.Feed, u.Requests, u.Bytes, u.CacheHits))
		req, bytes, hits = req+u.Requests, bytes+u.Bytes, hits+u.CacheHits
	}
	preview := strings.Join(parts, "; ")
	if len(preview) > 500 {
		preview = preview[:500]
	}
	return runner.New(s.pool).InsertServerEvent(ctx, runner.InsertServerEventParams{RunID: runID, Type: "pkg_summary", Name: "package proxy",
		Status: fmt.Sprintf("%d requests", req), TokensIn: bytes, TokensOut: hits, Preview: preview})
}

// plainPath reports whether no segment of p is "." or "..", also when
// percent-encoded, and no segment holds an encoded slash or a backslash.
func plainPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if d, err := url.PathUnescape(seg); err == nil {
			seg = d
		}
		if seg == "." || seg == ".." || strings.ContainsAny(seg, "/\\") {
			return false
		}
	}
	return true
}
