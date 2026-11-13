// Package feeds holds private package feeds, the package proxy that agent
// runs reach with their run token, and its cache (§8.11, D-18, D-23).
package feeds

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/secrets"
	"github.com/alternayte/sayso/internal/storage"
)

// Feed kinds.
const (
	KindNPM     = "npm"
	KindNuGet   = "nuget"
	KindPyPI    = "pypi"
	KindMaven   = "maven"
	KindGoProxy = "goproxy"
	KindGitHub  = "github"
)

// Modes and authentication styles.
const (
	ModeProxy  = "proxy"
	ModeInject = "inject"
	AuthBasic  = "basic"
	AuthBearer = "bearer"
	AuthHeader = "header"
)

// Default cache limits (§8.11): 5 GB, or 2 GB when the default store is postgres.
const (
	DefaultCacheBytes  = 5 << 30
	PostgresCacheBytes = 2 << 30
)

// Service is the feeds feature.
type Service struct {
	pool        *pgxpool.Pool
	secrets     *secrets.Store
	stores      *storage.Registry
	http        *http.Client
	log         *slog.Logger
	redact      func(string) string
	proxyURL    string
	gitHubToken func(ctx context.Context, repos []string) (token, webURL string, err error)
}

// NewService builds the feature. proxyURL is the API URL as agent runs see
// it. gitHubToken mints a contents:read token for the listed repositories.
func NewService(pool *pgxpool.Pool, sec *secrets.Store, stores *storage.Registry, hc *http.Client, log *slog.Logger, redact func(string) string,
	proxyURL string, gitHubToken func(ctx context.Context, repos []string) (string, string, error)) *Service {
	return &Service{pool: pool, secrets: sec, stores: stores, http: hc, log: log, redact: redact, proxyURL: strings.TrimRight(proxyURL, "/"), gitHubToken: gitHubToken}
}

func (s *Service) proxyBase(feed string) string { return s.proxyURL + "/pkg/" + feed }

func secretName(feed string) string { return "feed." + feed + ".secret" }

// Access is the metadata and actor of the operations.
type Access struct {
	Admin map[string]any
	Actor func(context.Context) audit.Actor
}

type FeedInput struct {
	Name          string         `json:"name"`
	Kind          string         `json:"kind" enum:"npm,nuget,pypi,maven,goproxy,github"`
	UpstreamURL   string         `json:"upstream_url,omitempty"`
	AuthStyle     string         `json:"auth_style,omitempty" enum:"basic,bearer,header"`
	AuthHeader    string         `json:"auth_header,omitempty"`
	Username      string         `json:"username,omitempty"`
	Secret        *secrets.Input `json:"secret,omitempty"`
	Mode          string         `json:"mode,omitempty" enum:"proxy,inject"`
	MatchURLs     []string       `json:"match_urls,omitempty" doc:"Upstream URLs as repository files write them"`
	Repos         []string       `json:"repos,omitempty" doc:"Kind github: owner/name of the readable repositories"`
	Allowed       []string       `json:"allowed_repositories,omitempty" doc:"Empty: all repositories"`
	ConfirmInject bool           `json:"confirm_inject,omitempty" doc:"Inject mode gives the credential to agent runs; confirm it"`
	CacheEnabled  *bool          `json:"cache_enabled,omitempty"`
	CacheMaxMB    int64          `json:"cache_max_mb,omitempty" minimum:"0"`
}

type FeedBody struct {
	Name              string        `json:"name"`
	Kind              string        `json:"kind"`
	UpstreamURL       string        `json:"upstream_url"`
	AuthStyle         string        `json:"auth_style"`
	AuthHeader        string        `json:"auth_header"`
	Username          string        `json:"username"`
	Secret            secrets.Field `json:"secret"`
	Mode              string        `json:"mode"`
	CredentialExposed bool          `json:"credential_exposed" doc:"Inject mode: the credential reaches agent runs"`
	InjectConfirmedBy string        `json:"inject_confirmed_by"`
	MatchURLs         []string      `json:"match_urls"`
	Repos             []string      `json:"repos"`
	Allowed           []string      `json:"allowed_repositories"`
	CacheEnabled      bool          `json:"cache_enabled"`
	CacheMaxMB        int64         `json:"cache_max_mb"`
	ProxyURL          string        `json:"proxy_url"`
	CheckedAt         *time.Time    `json:"checked_at"`
}

func (s *Service) body(ctx context.Context, f PackageFeed) (FeedBody, error) {
	sec, err := s.secrets.Describe(ctx, f.SecretID)
	b := FeedBody{Name: f.Name, Kind: f.Kind, UpstreamURL: f.UpstreamUrl, AuthStyle: f.AuthStyle, AuthHeader: f.AuthHeader, Username: f.Username,
		Secret: sec, Mode: f.Mode, CredentialExposed: f.Mode == ModeInject, InjectConfirmedBy: f.InjectConfirmedBy, MatchURLs: nonNil(f.MatchUrls),
		Repos: nonNil(f.Repos), Allowed: nonNil(f.AllowedRepositories), CacheEnabled: f.CacheEnabled, CacheMaxMB: f.CacheMaxBytes >> 20,
		ProxyURL: s.proxyBase(f.Name), CheckedAt: f.CheckedAt}
	return b, err
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

var feedName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (in FeedInput) check() error {
	if !feedName.MatchString(in.Name) {
		return errors.New("name: use lower-case letters, digits and hyphens")
	}
	if in.Kind == KindGitHub {
		if len(in.Repos) == 0 {
			return errors.New("repos: kind github needs one or more owner/name repositories")
		}
		return nil
	}
	u, err := url.Parse(in.UpstreamURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("upstream_url: give an http or https URL")
	}
	if in.AuthStyle == AuthHeader && in.AuthHeader == "" {
		return errors.New("auth_header: give the header name")
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return huma.Error404NotFound("feed not found")
	}
	return err
}

// Register adds the feed operations for admins (REQ-PKG-001, REQ-PKG-004).
func (s *Service) Register(api huma.API, acc Access) {
	tags := []string{"feeds"}
	huma.Register(api, huma.Operation{OperationID: "list-feeds", Method: http.MethodGet, Path: "/feeds", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []FeedBody }, error) {
			rows, err := New(s.pool).List(ctx)
			if err != nil {
				return nil, err
			}
			out := []FeedBody{}
			for _, f := range rows {
				b, err := s.body(ctx, f)
				if err != nil {
					return nil, err
				}
				out = append(out, b)
			}
			return &struct{ Body []FeedBody }{out}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "put-feed", Method: http.MethodPut, Path: "/feeds/{name}", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
			Body FeedInput
		}) (*struct{ Body FeedBody }, error) {
			in.Body.Name = in.Name
			b, err := s.save(ctx, acc.Actor(ctx), in.Body)
			if err != nil {
				return nil, err
			}
			return &struct{ Body FeedBody }{b}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "delete-feed", Method: http.MethodDelete, Path: "/feeds/{name}", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{}, error) {
			cur, err := New(s.pool).Get(ctx, in.Name)
			if err != nil {
				return nil, notFound(err)
			}
			actor := acc.Actor(ctx)
			err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE repositories SET feeds = array_remove(feeds, $1)`, in.Name); err != nil {
					return err
				}
				if err := New(tx).DeleteFeed(ctx, in.Name); err != nil {
					return err
				}
				return audit.Write(ctx, tx, actor, "feed.delete", in.Name, nil)
			})
			if err == nil && cur.SecretID != nil {
				err = s.secrets.Remove(ctx, actor, secretName(in.Name))
			}
			return nil, err
		})

	huma.Register(api, huma.Operation{OperationID: "test-feed", Method: http.MethodPost, Path: "/feeds/{name}/test", Tags: tags, Metadata: acc.Admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct {
			Body struct {
				OK     bool   `json:"ok"`
				Detail string `json:"detail"`
			}
		}, error) {
			out := &struct {
				Body struct {
					OK     bool   `json:"ok"`
					Detail string `json:"detail"`
				}
			}{}
			detail, err := s.Test(ctx, in.Name)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound(err)
			}
			out.Body.OK, out.Body.Detail = err == nil, detail
			if err != nil {
				out.Body.Detail = s.redact(err.Error())
			}
			return out, nil
		})
}

func (s *Service) save(ctx context.Context, actor audit.Actor, in FeedInput) (FeedBody, error) {
	if err := in.check(); err != nil {
		return FeedBody{}, huma.Error422UnprocessableEntity(err.Error())
	}
	q := New(s.pool)
	cur, curErr := q.Get(ctx, in.Name)
	if curErr != nil && !errors.Is(curErr, pgx.ErrNoRows) {
		return FeedBody{}, curErr
	}
	if in.Mode == "" {
		in.Mode = ModeProxy
	}
	if in.AuthStyle == "" {
		in.AuthStyle = AuthBearer
	}
	confirmedBy := ""
	if in.Mode == ModeInject {
		switch {
		case in.ConfirmInject:
			confirmedBy = actor.Login
		case curErr == nil && cur.Mode == ModeInject:
			confirmedBy = cur.InjectConfirmedBy
		default:
			return FeedBody{}, huma.Error422UnprocessableEntity("inject mode gives the feed credential to agent runs: set confirm_inject to confirm it")
		}
	}
	sid, err := s.secrets.Apply(ctx, actor, secretName(in.Name), in.Secret, cur.SecretID)
	if err != nil {
		return FeedBody{}, huma.Error422UnprocessableEntity(err.Error())
	}
	cacheOn := true
	if in.CacheEnabled != nil {
		cacheOn = *in.CacheEnabled
	}
	limit := in.CacheMaxMB << 20
	if limit == 0 {
		limit = DefaultCacheBytes
		if id, _, err := s.stores.Default(ctx); err == nil {
			if row, err := storage.New(s.pool).StoreByID(ctx, id); err == nil && row.Driver == "postgres" {
				limit = PostgresCacheBytes
			}
		}
	}
	var allowed []string
	if len(in.Allowed) > 0 {
		allowed = in.Allowed
	}
	var row PackageFeed
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		row, err = New(tx).Upsert(ctx, UpsertParams{Name: in.Name, Kind: in.Kind, UpstreamUrl: in.UpstreamURL, AuthStyle: in.AuthStyle, AuthHeader: in.AuthHeader,
			Username: in.Username, SecretID: sid, Mode: in.Mode, MatchUrls: nonNil(in.MatchURLs), Repos: nonNil(in.Repos), AllowedRepositories: allowed,
			InjectConfirmedBy: confirmedBy, CacheEnabled: cacheOn, CacheMaxBytes: limit})
		if err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, actor, "feed.put", in.Name, map[string]any{"kind": in.Kind, "mode": in.Mode, "upstream_url": in.UpstreamURL}); err != nil {
			return err
		}
		if in.Mode == ModeInject && in.ConfirmInject {
			return audit.Write(ctx, tx, actor, "feed.inject_confirmed", in.Name, map[string]any{"warning": "credential exposed to agent runs"})
		}
		return nil
	})
	if err != nil {
		return FeedBody{}, err
	}
	return s.body(ctx, row)
}

// Test fetches one metadata URL of a feed with its credential (§8.11).
func (s *Service) Test(ctx context.Context, name string) (string, error) {
	f, err := New(s.pool).Get(ctx, name)
	if err != nil {
		return "", err
	}
	path := "/"
	switch f.Kind {
	case KindNuGet:
		path = ""
	case KindGitHub:
		path = "/" + f.Repos[0] + ".git/info/refs"
	case KindGoProxy:
		path = "/"
	}
	u, auth, err := s.upstream(ctx, f, path)
	if err != nil {
		return "", err
	}
	if f.Kind == KindGitHub {
		u += "?service=git-upload-pack"
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if auth != "" {
		req.Header.Set(f.headerName(), auth)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
	}
	_ = New(s.pool).SetChecked(ctx, name)
	return fmt.Sprintf("GET %s: status %d", u, resp.StatusCode), nil
}

// SpecFeeds returns the feed entries of a run for a repository: only the
// feeds that the repository may use. Inject mode alone carries the
// credential (SI-01).
func (s *Service) SpecFeeds(ctx context.Context, repoFullName string, names []string) ([]runner.SpecFeed, error) {
	var out []runner.SpecFeed
	for _, n := range names {
		f, err := New(s.pool).Get(ctx, n)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if f.AllowedRepositories != nil && !slices.ContainsFunc(f.AllowedRepositories, func(r string) bool { return strings.EqualFold(r, repoFullName) }) {
			continue
		}
		e := runner.SpecFeed{Name: f.Name, Kind: f.Kind, Mode: f.Mode, URL: s.proxyBase(f.Name), MatchURLs: nonNil(f.MatchUrls), Repos: nonNil(f.Repos)}
		if f.Mode == ModeInject {
			e.URL = f.UpstreamUrl
			v, err := s.secrets.ValueOf(ctx, f.SecretID)
			if err != nil {
				return nil, err
			}
			e.Credential, e.Username, e.AuthStyle = v, f.Username, f.AuthStyle
			// The runtime writes a Basic credential encoded, as npm _auth does.
			if f.AuthStyle == AuthBasic {
				s.secrets.Learn(secretName(f.Name), base64.StdEncoding.EncodeToString([]byte(f.Username+":"+v)))
			}
		}
		out = append(out, e)
	}
	return out, nil
}
