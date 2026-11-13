// Package config exports the configuration in PostgreSQL as YAML and
// applies YAML back in one transaction (§16.1, D-10). Secret fields hold a
// secret name or an environment variable name, never a value.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.yaml.in/yaml/v3"
)

// Version is the document version this code reads and writes.
const Version = 1

// Secret is a secret field: the name of a stored secret, or the name of an
// environment variable of the API process.
type Secret struct {
	Secret string `yaml:"secret,omitempty" json:"secret,omitempty"`
	Env    string `yaml:"env,omitempty" json:"env,omitempty"`
}

type Provider struct {
	Name            string         `yaml:"name"`
	APIType         string         `yaml:"api_type"`
	BaseURL         string         `yaml:"base_url"`
	AuthStyle       string         `yaml:"auth_style"`
	AuthHeader      string         `yaml:"auth_header,omitempty"`
	Key             *Secret        `yaml:"key,omitempty"`
	Headers         map[string]any `yaml:"headers"`
	Compat          map[string]any `yaml:"compat"`
	Models          []string       `yaml:"models"`
	MaxConcurrency  int            `yaml:"max_concurrency"`
	TokensPerMinute int            `yaml:"tokens_per_minute"`
}

type Profile struct {
	Name           string           `yaml:"name"`
	Targets        []map[string]any `yaml:"targets"`
	RoundBudgetUSD float64          `yaml:"round_budget_usd"`
}

type MCPServer struct {
	Name         string         `yaml:"name"`
	URL          string         `yaml:"url"`
	Headers      map[string]any `yaml:"headers"`
	SecretHeader string         `yaml:"secret_header,omitempty"`
	Secret       *Secret        `yaml:"secret,omitempty"`
	AllowTools   []string       `yaml:"allow_tools"`
	AllowWrite   bool           `yaml:"allow_write"`
}

type Extension struct {
	Name         string            `yaml:"name"`
	Kind         string            `yaml:"kind"`
	Spec         string            `yaml:"spec"`
	Env          map[string]any    `yaml:"env"`
	Secrets      map[string]Secret `yaml:"secrets"`
	Enabled      bool              `yaml:"enabled"`
	Repositories []string          `yaml:"repositories"`
}

type Feed struct {
	Name                string    `yaml:"name"`
	Kind                string    `yaml:"kind"`
	UpstreamURL         string    `yaml:"upstream_url"`
	AuthStyle           string    `yaml:"auth_style"`
	AuthHeader          string    `yaml:"auth_header,omitempty"`
	Username            string    `yaml:"username,omitempty"`
	Secret              *Secret   `yaml:"secret,omitempty"`
	Mode                string    `yaml:"mode"`
	MatchURLs           []string  `yaml:"match_urls"`
	Repos               []string  `yaml:"repos"`
	AllowedRepositories *[]string `yaml:"allowed_repositories,omitempty"`
	CacheEnabled        bool      `yaml:"cache_enabled"`
	CacheMaxBytes       int64     `yaml:"cache_max_bytes"`
}

type Store struct {
	Name          string         `yaml:"name"`
	Driver        string         `yaml:"driver"`
	Config        map[string]any `yaml:"config"`
	Secret        *Secret        `yaml:"secret,omitempty"`
	RetentionDays int            `yaml:"retention_days"`
	Default       bool           `yaml:"default"`
}

type Profiles struct {
	Review    string `yaml:"review"`
	Challenge string `yaml:"challenge"`
	Reply     string `yaml:"reply"`
	Fix       string `yaml:"fix"`
}

type Repository struct {
	FullName             string   `yaml:"full_name"`
	Mode                 string   `yaml:"mode"`
	Profiles             Profiles `yaml:"profiles"`
	SkaelSet             string   `yaml:"skael_set"`
	AgentImage           string   `yaml:"agent_image"`
	MCPServers           []string `yaml:"mcp_servers"`
	Feeds                []string `yaml:"feeds"`
	MaxConcurrentRounds  *int     `yaml:"max_concurrent_rounds,omitempty"`
	Execution            bool     `yaml:"execution"`
	ChallengeMinSeverity string   `yaml:"challenge_min_severity"`
}

type TeamRole struct {
	Team string `yaml:"team"`
	Role string `yaml:"role"`
}

type AgentImage struct {
	Name    string `yaml:"name"`
	Image   string `yaml:"image"`
	Default bool   `yaml:"default"`
}

// Doc is the configuration document. Apply leaves a section that is absent
// from the document unchanged; a present section is complete, so apply
// removes entries that it does not list. Repositories are never removed.
type Doc struct {
	Version      int            `yaml:"version"`
	Settings     map[string]any `yaml:"settings"`
	Providers    []Provider     `yaml:"providers"`
	Profiles     []Profile      `yaml:"profiles"`
	MCPServers   []MCPServer    `yaml:"mcp_servers"`
	Extensions   []Extension    `yaml:"extensions"`
	PackageFeeds []Feed         `yaml:"package_feeds"`
	Stores       []Store        `yaml:"stores"`
	AgentImages  []AgentImage   `yaml:"agent_images"`
	Repositories []Repository   `yaml:"repositories"`
	TeamRoles    []TeamRole     `yaml:"team_roles"`
}

// DB is a pool or a transaction.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Marshal writes the document as YAML.
func Marshal(d Doc) ([]byte, error) { return yaml.Marshal(d) }

// Parse reads a YAML document and rejects unknown fields.
func Parse(b []byte) (Doc, error) {
	var d Doc
	dec := yaml.NewDecoder(bytesReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return d, fmt.Errorf("config: %w", err)
	}
	if d.Version != Version {
		return d, fmt.Errorf("config: version %d is not supported; use version %d", d.Version, Version)
	}
	return d, nil
}

func secretRef(ctx context.Context, db DB, id *uuid.UUID) (*Secret, error) {
	if id == nil {
		return nil, nil
	}
	var name, kind string
	var env *string
	err := db.QueryRow(ctx, `SELECT name, kind, env_name FROM secrets WHERE id = $1`, *id).Scan(&name, &kind, &env)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if kind == "env_ref" && env != nil {
		return &Secret{Env: *env}, nil
	}
	return &Secret{Secret: name}, nil
}

func obj(raw []byte) (map[string]any, error) {
	m := map[string]any{}
	if len(raw) == 0 {
		return m, nil
	}
	return m, json.Unmarshal(raw, &m)
}

func list[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// Export reads the configuration.
func Export(ctx context.Context, db DB) (Doc, error) {
	d := Doc{Version: Version, Settings: map[string]any{}}
	if err := scan(ctx, db, `SELECT key, value FROM settings ORDER BY key`, func(r pgx.Rows) error {
		var k string
		var v []byte
		if err := r.Scan(&k, &v); err != nil {
			return err
		}
		var x any
		if err := json.Unmarshal(v, &x); err != nil {
			return err
		}
		d.Settings[k] = x
		return nil
	}); err != nil {
		return d, err
	}
	type withSecret struct {
		id  *uuid.UUID
		set func(*Secret)
	}
	var refs []withSecret
	if err := scan(ctx, db, `SELECT name, api_type, base_url, auth_style, auth_header, secret_id, headers, compat, models, max_concurrency, tokens_per_minute FROM providers ORDER BY name`, func(r pgx.Rows) error {
		var p Provider
		var id *uuid.UUID
		var h, c []byte
		if err := r.Scan(&p.Name, &p.APIType, &p.BaseURL, &p.AuthStyle, &p.AuthHeader, &id, &h, &c, &p.Models, &p.MaxConcurrency, &p.TokensPerMinute); err != nil {
			return err
		}
		var err error
		if p.Headers, err = obj(h); err != nil {
			return err
		}
		if p.Compat, err = obj(c); err != nil {
			return err
		}
		p.Models = list(p.Models)
		d.Providers = append(d.Providers, p)
		i := len(d.Providers) - 1
		refs = append(refs, withSecret{id, func(s *Secret) { d.Providers[i].Key = s }})
		return nil
	}); err != nil {
		return d, err
	}
	if err := scan(ctx, db, `SELECT name, targets, round_budget_usd::float8 FROM profiles ORDER BY name`, func(r pgx.Rows) error {
		var p Profile
		var t []byte
		if err := r.Scan(&p.Name, &t, &p.RoundBudgetUSD); err != nil {
			return err
		}
		if err := json.Unmarshal(t, &p.Targets); err != nil {
			return err
		}
		p.Targets = list(p.Targets)
		d.Profiles = append(d.Profiles, p)
		return nil
	}); err != nil {
		return d, err
	}
	if err := scan(ctx, db, `SELECT name, url, headers, secret_header, secret_id, allow_tools, allow_write FROM mcp_servers ORDER BY name`, func(r pgx.Rows) error {
		var m MCPServer
		var id *uuid.UUID
		var h []byte
		if err := r.Scan(&m.Name, &m.URL, &h, &m.SecretHeader, &id, &m.AllowTools, &m.AllowWrite); err != nil {
			return err
		}
		var err error
		if m.Headers, err = obj(h); err != nil {
			return err
		}
		m.AllowTools = list(m.AllowTools)
		d.MCPServers = append(d.MCPServers, m)
		i := len(d.MCPServers) - 1
		refs = append(refs, withSecret{id, func(s *Secret) { d.MCPServers[i].Secret = s }})
		return nil
	}); err != nil {
		return d, err
	}
	extSecrets := map[int]map[string]string{}
	if err := scan(ctx, db, `SELECT name, kind, spec, env, secret_ids, enabled, repositories FROM extensions ORDER BY name`, func(r pgx.Rows) error {
		var x Extension
		var env, ids []byte
		if err := r.Scan(&x.Name, &x.Kind, &x.Spec, &env, &ids, &x.Enabled, &x.Repositories); err != nil {
			return err
		}
		var err error
		if x.Env, err = obj(env); err != nil {
			return err
		}
		m := map[string]string{}
		if len(ids) > 0 {
			if err := json.Unmarshal(ids, &m); err != nil {
				return err
			}
		}
		x.Repositories = list(x.Repositories)
		x.Secrets = map[string]Secret{}
		d.Extensions = append(d.Extensions, x)
		extSecrets[len(d.Extensions)-1] = m
		return nil
	}); err != nil {
		return d, err
	}
	for i, m := range extSecrets {
		for env, sid := range m {
			id, err := uuid.Parse(sid)
			if err != nil {
				return d, fmt.Errorf("extension %s secret %s: %w", d.Extensions[i].Name, env, err)
			}
			s, err := secretRef(ctx, db, &id)
			if err != nil {
				return d, err
			}
			if s != nil {
				d.Extensions[i].Secrets[env] = *s
			}
		}
	}
	if err := scan(ctx, db, `SELECT name, kind, upstream_url, auth_style, auth_header, username, secret_id, mode, match_urls, repos, allowed_repositories, cache_enabled, cache_max_bytes FROM package_feeds ORDER BY name`, func(r pgx.Rows) error {
		var f Feed
		var id *uuid.UUID
		var allowed []string
		if err := r.Scan(&f.Name, &f.Kind, &f.UpstreamURL, &f.AuthStyle, &f.AuthHeader, &f.Username, &id, &f.Mode, &f.MatchURLs, &f.Repos, &allowed, &f.CacheEnabled, &f.CacheMaxBytes); err != nil {
			return err
		}
		if allowed != nil {
			f.AllowedRepositories = &allowed
		}
		f.MatchURLs, f.Repos = list(f.MatchURLs), list(f.Repos)
		d.PackageFeeds = append(d.PackageFeeds, f)
		i := len(d.PackageFeeds) - 1
		refs = append(refs, withSecret{id, func(s *Secret) { d.PackageFeeds[i].Secret = s }})
		return nil
	}); err != nil {
		return d, err
	}
	if err := scan(ctx, db, `SELECT name, driver, config, secret_id, retention_days, is_default FROM stores ORDER BY name`, func(r pgx.Rows) error {
		var s Store
		var id *uuid.UUID
		var c []byte
		if err := r.Scan(&s.Name, &s.Driver, &c, &id, &s.RetentionDays, &s.Default); err != nil {
			return err
		}
		var err error
		if s.Config, err = obj(c); err != nil {
			return err
		}
		d.Stores = append(d.Stores, s)
		i := len(d.Stores) - 1
		refs = append(refs, withSecret{id, func(x *Secret) { d.Stores[i].Secret = x }})
		return nil
	}); err != nil {
		return d, err
	}
	if err := scan(ctx, db, `SELECT name, image, is_default FROM agent_images ORDER BY name`, func(r pgx.Rows) error {
		var a AgentImage
		if err := r.Scan(&a.Name, &a.Image, &a.Default); err != nil {
			return err
		}
		d.AgentImages = append(d.AgentImages, a)
		return nil
	}); err != nil {
		return d, err
	}
	if err := scan(ctx, db, `SELECT full_name, mode, profile_review, profile_challenge, profile_reply, profile_fix, skael_set, agent_image, mcp_servers, feeds,
			max_concurrent_rounds, execution, challenge_min_severity FROM repositories WHERE removed_at IS NULL ORDER BY lower(full_name)`, func(r pgx.Rows) error {
		var x Repository
		var max *int32
		if err := r.Scan(&x.FullName, &x.Mode, &x.Profiles.Review, &x.Profiles.Challenge, &x.Profiles.Reply, &x.Profiles.Fix, &x.SkaelSet, &x.AgentImage,
			&x.MCPServers, &x.Feeds, &max, &x.Execution, &x.ChallengeMinSeverity); err != nil {
			return err
		}
		if max != nil {
			n := int(*max)
			x.MaxConcurrentRounds = &n
		}
		x.MCPServers, x.Feeds = list(x.MCPServers), list(x.Feeds)
		d.Repositories = append(d.Repositories, x)
		return nil
	}); err != nil {
		return d, err
	}
	if err := scan(ctx, db, `SELECT team_slug, role FROM team_roles ORDER BY team_slug`, func(r pgx.Rows) error {
		var t TeamRole
		if err := r.Scan(&t.Team, &t.Role); err != nil {
			return err
		}
		d.TeamRoles = append(d.TeamRoles, t)
		return nil
	}); err != nil {
		return d, err
	}
	for _, r := range refs {
		s, err := secretRef(ctx, db, r.id)
		if err != nil {
			return d, err
		}
		r.set(s)
	}
	d.Providers, d.Profiles, d.MCPServers, d.Extensions = list(d.Providers), list(d.Profiles), list(d.MCPServers), list(d.Extensions)
	d.PackageFeeds, d.Stores, d.AgentImages = list(d.PackageFeeds), list(d.Stores), list(d.AgentImages)
	d.Repositories, d.TeamRoles = list(d.Repositories), list(d.TeamRoles)
	return d, nil
}

func scan(ctx context.Context, db DB, q string, row func(pgx.Rows) error) error {
	rows, err := db.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := row(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func names[T any](v []T, name func(T) string) ([]string, error) {
	out := make([]string, 0, len(v))
	for _, x := range v {
		n := name(x)
		if n == "" {
			return nil, errors.New("an entry has no name")
		}
		if slices.Contains(out, n) {
			return nil, fmt.Errorf("the name %s occurs two times", n)
		}
		out = append(out, n)
	}
	return out, nil
}
