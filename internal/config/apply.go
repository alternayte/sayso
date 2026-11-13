package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/models"
	"github.com/alternayte/sayso/internal/settings"
)

// ErrChanged means the configuration changed between the diff that the
// operator confirmed and the apply.
var ErrChanged = errors.New("the configuration changed after the diff; run the diff again")

// Service exports and applies the configuration.
type Service struct {
	Pool *pgxpool.Pool
	// LearnEnv registers the value of an environment variable that a new
	// secret reference names, so redaction hides it.
	LearnEnv func(name, envName string)
}

// Export reads the configuration as YAML.
func (s *Service) Export(ctx context.Context) ([]byte, error) {
	d, err := Export(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	return Marshal(d)
}

// Plan parses YAML and returns the changes that apply would make.
func (s *Service) Plan(ctx context.Context, b []byte) ([]ConfigChange, error) {
	next, err := Parse(b)
	if err != nil {
		return nil, err
	}
	cur, err := Export(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	changes := Diff(cur, next)
	return changes, validate(ctx, s.Pool, cur, next)
}

// Apply applies YAML in one transaction. When expect is not nil, the
// changes must equal it, so the operator applies what they confirmed.
func (s *Service) Apply(ctx context.Context, by audit.Actor, b []byte, expect []ConfigChange) ([]ConfigChange, error) {
	next, err := Parse(b)
	if err != nil {
		return nil, err
	}
	var changes []ConfigChange
	var envs [][2]string
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('sayso.config.apply'))`); err != nil {
			return err
		}
		cur, err := Export(ctx, tx)
		if err != nil {
			return err
		}
		changes = Diff(cur, next)
		if expect != nil && !reflect.DeepEqual(changes, expect) {
			return ErrChanged
		}
		if err := validate(ctx, tx, cur, next); err != nil {
			return err
		}
		a := &applier{tx: tx, by: by}
		if err := a.apply(ctx, next, changes); err != nil {
			return err
		}
		envs = a.envs
		return audit.Write(ctx, tx, by, "config.apply", "config", map[string]any{"changes": len(changes)})
	})
	if err != nil {
		return nil, err
	}
	for _, e := range envs {
		if s.LearnEnv != nil {
			s.LearnEnv(e[0], e[1])
		}
	}
	return changes, nil
}

// validate checks references between sections and the secret names.
func validate(ctx context.Context, db DB, cur, next Doc) error {
	pick := func(a, b any) any {
		if reflect.ValueOf(b).IsNil() {
			return a
		}
		return b
	}
	providers := pick(cur.Providers, next.Providers).([]Provider)
	profiles := pick(cur.Profiles, next.Profiles).([]Profile)
	mcp := pick(cur.MCPServers, next.MCPServers).([]MCPServer)
	feeds := pick(cur.PackageFeeds, next.PackageFeeds).([]Feed)
	images := pick(cur.AgentImages, next.AgentImages).([]AgentImage)
	stores := pick(cur.Stores, next.Stores).([]Store)
	pn, err := names(providers, func(x Provider) string { return x.Name })
	if err != nil {
		return fmt.Errorf("providers: %w", err)
	}
	fn, err := names(profiles, func(x Profile) string { return x.Name })
	if err != nil {
		return fmt.Errorf("profiles: %w", err)
	}
	for _, p := range models.DefaultProfiles {
		if !slices.Contains(fn, p) {
			return fmt.Errorf("profiles: the default profile %s is missing", p)
		}
	}
	mn, err := names(mcp, func(x MCPServer) string { return x.Name })
	if err != nil {
		return fmt.Errorf("mcp_servers: %w", err)
	}
	dn, err := names(feeds, func(x Feed) string { return x.Name })
	if err != nil {
		return fmt.Errorf("package_feeds: %w", err)
	}
	in, err := names(images, func(x AgentImage) string { return x.Name })
	if err != nil {
		return fmt.Errorf("agent_images: %w", err)
	}
	if _, err := names(stores, func(x Store) string { return x.Name }); err != nil {
		return fmt.Errorf("stores: %w", err)
	}
	if n := countTrue(stores, func(x Store) bool { return x.Default }); n != 1 {
		return fmt.Errorf("stores: %d stores are the default; set exactly one", n)
	}
	if n := countTrue(images, func(x AgentImage) bool { return x.Default }); n > 1 {
		return fmt.Errorf("agent_images: %d images are the default; set one or none", n)
	}
	if _, err := names(next.Extensions, func(x Extension) string { return x.Name }); err != nil {
		return fmt.Errorf("extensions: %w", err)
	}
	if _, err := names(next.TeamRoles, func(x TeamRole) string { return x.Team }); err != nil {
		return fmt.Errorf("team_roles: %w", err)
	}
	for _, p := range profiles {
		raw, _ := json.Marshal(p.Targets)
		var ts []models.Target
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ts); err != nil {
			return fmt.Errorf("profile %s targets: %w", p.Name, err)
		}
		for i, t := range ts {
			if t.Model == "" || !slices.Contains(pn, t.Provider) {
				return fmt.Errorf("profile %s target %d: provider %q does not exist or the model is empty", p.Name, i+1, t.Provider)
			}
		}
	}
	for _, r := range next.Repositories {
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM repositories WHERE lower(full_name) = lower($1) AND removed_at IS NULL`, r.FullName).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("repository %s is not installed; install the GitHub App on it first", r.FullName)
		}
		for _, p := range []string{r.Profiles.Review, r.Profiles.Challenge, r.Profiles.Reply, r.Profiles.Fix} {
			if !slices.Contains(fn, p) {
				return fmt.Errorf("repository %s: profile %s does not exist", r.FullName, p)
			}
		}
		for _, m := range r.MCPServers {
			if !slices.Contains(mn, m) {
				return fmt.Errorf("repository %s: MCP server %s does not exist", r.FullName, m)
			}
		}
		for _, f := range r.Feeds {
			if !slices.Contains(dn, f) {
				return fmt.Errorf("repository %s: package feed %s does not exist", r.FullName, f)
			}
		}
		if r.AgentImage != "" && !slices.Contains(in, r.AgentImage) {
			return fmt.Errorf("repository %s: agent image %s does not exist", r.FullName, r.AgentImage)
		}
	}
	var check []Secret
	add := func(s *Secret) {
		if s != nil {
			check = append(check, *s)
		}
	}
	for _, p := range next.Providers {
		add(p.Key)
	}
	for _, m := range next.MCPServers {
		add(m.Secret)
	}
	for _, f := range next.PackageFeeds {
		add(f.Secret)
	}
	for _, s := range next.Stores {
		add(s.Secret)
	}
	for _, x := range next.Extensions {
		for _, s := range x.Secrets {
			add(&s)
		}
	}
	for _, s := range check {
		if (s.Secret == "") == (s.Env == "") {
			return errors.New("a secret field needs exactly one of secret or env")
		}
		if s.Secret == "" {
			continue
		}
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM secrets WHERE name = $1`, s.Secret).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("secret %s does not exist; store it before you apply", s.Secret)
		}
	}
	return nil
}

func countTrue[T any](v []T, f func(T) bool) int {
	n := 0
	for _, x := range v {
		if f(x) {
			n++
		}
	}
	return n
}

type applier struct {
	tx   pgx.Tx
	by   audit.Actor
	envs [][2]string
}

// secret returns the secret ID of a field. An env reference is stored
// under the name that the UI uses for the field.
func (a *applier) secret(ctx context.Context, name string, s *Secret) (*uuid.UUID, error) {
	if s == nil {
		return nil, nil
	}
	var id uuid.UUID
	if s.Secret != "" {
		err := a.tx.QueryRow(ctx, `SELECT id FROM secrets WHERE name = $1`, s.Secret).Scan(&id)
		return &id, err
	}
	err := a.tx.QueryRow(ctx, `INSERT INTO secrets (id, name, kind, env_name, last4, updated_by) VALUES ($1, $2, 'env_ref', $3, '', $4)
		ON CONFLICT (name) DO UPDATE SET kind = 'env_ref', ciphertext = NULL, nonce = NULL, key_version = NULL,
		env_name = EXCLUDED.env_name, last4 = '', updated_by = EXCLUDED.updated_by, updated_at = now() RETURNING id`,
		uuid.Must(uuid.NewV7()), name, s.Env, a.by.Login).Scan(&id)
	if err != nil {
		return nil, err
	}
	a.envs = append(a.envs, [2]string{name, s.Env})
	return &id, audit.Write(ctx, a.tx, a.by, "secrets.put", name, map[string]any{"kind": "env_ref", "env": s.Env})
}

func jsonb(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (a *applier) apply(ctx context.Context, d Doc, changes []ConfigChange) error {
	touched := map[string]map[string]string{}
	for _, c := range changes {
		if touched[c.Section] == nil {
			touched[c.Section] = map[string]string{}
		}
		touched[c.Section][c.Name] = c.Op
	}
	op := func(section, name string) string { return touched[section][name] }
	for _, k := range sortedKeys(touched["settings"]) {
		if _, err := settings.SetTx(ctx, a.tx, a.by, k, jsonb(d.Settings[k])); err != nil {
			return err
		}
	}
	for _, p := range d.Providers {
		if op("providers", p.Name) == "" {
			continue
		}
		sid, err := a.secret(ctx, "provider."+p.Name+".key", p.Key)
		if err != nil {
			return err
		}
		if _, err := a.tx.Exec(ctx, `INSERT INTO providers (name, api_type, base_url, auth_style, auth_header, secret_id, headers, compat, models, max_concurrency, tokens_per_minute)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (name) DO UPDATE SET api_type = $2, base_url = $3, auth_style = $4, auth_header = $5, secret_id = $6, headers = $7, compat = $8,
			models = $9, max_concurrency = $10, tokens_per_minute = $11, updated_at = now()`,
			p.Name, p.APIType, p.BaseURL, p.AuthStyle, p.AuthHeader, sid, jsonb(orEmpty(p.Headers)), jsonb(orEmpty(p.Compat)), list(p.Models), p.MaxConcurrency, p.TokensPerMinute); err != nil {
			return fmt.Errorf("provider %s: %w", p.Name, err)
		}
	}
	for _, p := range d.Profiles {
		if op("profiles", p.Name) == "" {
			continue
		}
		if _, err := a.tx.Exec(ctx, `INSERT INTO profiles (name, targets, round_budget_usd) VALUES ($1, $2, $3)
			ON CONFLICT (name) DO UPDATE SET targets = $2, round_budget_usd = $3, updated_at = now()`, p.Name, jsonb(list(p.Targets)), p.RoundBudgetUSD); err != nil {
			return fmt.Errorf("profile %s: %w", p.Name, err)
		}
	}
	for _, m := range d.MCPServers {
		if op("mcp_servers", m.Name) == "" {
			continue
		}
		sid, err := a.secret(ctx, "mcp."+m.Name+".secret", m.Secret)
		if err != nil {
			return err
		}
		if _, err := a.tx.Exec(ctx, `INSERT INTO mcp_servers (name, url, headers, secret_header, secret_id, allow_tools, allow_write) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (name) DO UPDATE SET url = $2, headers = $3, secret_header = $4, secret_id = $5, allow_tools = $6, allow_write = $7, updated_at = now()`,
			m.Name, m.URL, jsonb(orEmpty(m.Headers)), m.SecretHeader, sid, list(m.AllowTools), m.AllowWrite); err != nil {
			return fmt.Errorf("MCP server %s: %w", m.Name, err)
		}
	}
	for _, f := range d.PackageFeeds {
		if op("package_feeds", f.Name) == "" {
			continue
		}
		sid, err := a.secret(ctx, "feed."+f.Name+".secret", f.Secret)
		if err != nil {
			return err
		}
		var allowed []string
		if f.AllowedRepositories != nil {
			allowed = list(*f.AllowedRepositories)
		}
		// The applying admin confirms inject mode (§13.5); the audit entry records it.
		confirmed := ""
		if f.Mode == "inject" {
			confirmed = a.by.Login
		}
		if _, err := a.tx.Exec(ctx, `INSERT INTO package_feeds (name, kind, upstream_url, auth_style, auth_header, username, secret_id, mode, match_urls, repos,
				allowed_repositories, inject_confirmed_by, cache_enabled, cache_max_bytes) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (name) DO UPDATE SET kind = $2, upstream_url = $3, auth_style = $4, auth_header = $5, username = $6, secret_id = $7, mode = $8, match_urls = $9,
			repos = $10, allowed_repositories = $11,
			inject_confirmed_by = CASE WHEN $8 = 'inject' AND package_feeds.mode = 'inject' AND package_feeds.inject_confirmed_by <> '' THEN package_feeds.inject_confirmed_by ELSE $12 END,
			cache_enabled = $13, cache_max_bytes = $14, updated_at = now()`,
			f.Name, f.Kind, f.UpstreamURL, f.AuthStyle, f.AuthHeader, f.Username, sid, f.Mode, list(f.MatchURLs), list(f.Repos), allowed, confirmed, f.CacheEnabled, f.CacheMaxBytes); err != nil {
			return fmt.Errorf("package feed %s: %w", f.Name, err)
		}
		if f.Mode == "inject" {
			if err := audit.Write(ctx, a.tx, a.by, "feeds.inject_confirm", f.Name, map[string]any{"source": "config.apply"}); err != nil {
				return err
			}
		}
	}
	for _, x := range d.Extensions {
		if op("extensions", x.Name) == "" {
			continue
		}
		ids := map[string]string{}
		for _, env := range sortedKeys(x.Secrets) {
			s := x.Secrets[env]
			id, err := a.secret(ctx, "extension."+x.Name+"."+strings.ToLower(env), &s)
			if err != nil {
				return err
			}
			ids[env] = id.String()
		}
		if _, err := a.tx.Exec(ctx, `INSERT INTO extensions (name, kind, spec, env, secret_ids, enabled, repositories) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (name) DO UPDATE SET kind = $2, spec = $3, env = $4, secret_ids = $5, enabled = $6, repositories = $7, updated_at = now()`,
			x.Name, x.Kind, x.Spec, jsonb(orEmpty(x.Env)), jsonb(ids), x.Enabled, list(x.Repositories)); err != nil {
			return fmt.Errorf("extension %s: %w", x.Name, err)
		}
	}
	if d.Stores != nil {
		if _, err := a.tx.Exec(ctx, `UPDATE stores SET is_default = false WHERE is_default AND name <> ALL($1)`, storeDefaults(d.Stores)); err != nil {
			return err
		}
	}
	for _, s := range d.Stores {
		if op("stores", s.Name) == "" {
			continue
		}
		sid, err := a.secret(ctx, "store."+s.Name+".secret", s.Secret)
		if err != nil {
			return err
		}
		if s.Default {
			if _, err := a.tx.Exec(ctx, `UPDATE stores SET is_default = false WHERE is_default AND name <> $1`, s.Name); err != nil {
				return err
			}
		}
		if _, err := a.tx.Exec(ctx, `INSERT INTO stores (id, name, driver, config, secret_id, retention_days, is_default) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (name) DO UPDATE SET driver = $3, config = $4, secret_id = $5, retention_days = $6, is_default = $7`,
			uuid.Must(uuid.NewV7()), s.Name, s.Driver, jsonb(orEmpty(s.Config)), sid, s.RetentionDays, s.Default); err != nil {
			return fmt.Errorf("store %s: %w", s.Name, err)
		}
	}
	for _, i := range d.AgentImages {
		if op("agent_images", i.Name) == "" {
			continue
		}
		if i.Default {
			if _, err := a.tx.Exec(ctx, `UPDATE agent_images SET is_default = false WHERE is_default AND name <> $1`, i.Name); err != nil {
				return err
			}
		}
		if _, err := a.tx.Exec(ctx, `INSERT INTO agent_images (name, image, is_default) VALUES ($1, $2, $3)
			ON CONFLICT (name) DO UPDATE SET image = $2, is_default = $3`, i.Name, i.Image, i.Default); err != nil {
			return fmt.Errorf("agent image %s: %w", i.Name, err)
		}
	}
	for _, r := range d.Repositories {
		if op("repositories", strings.ToLower(r.FullName)) == "" {
			continue
		}
		if _, err := a.tx.Exec(ctx, `UPDATE repositories SET mode = $2, profile_review = $3, profile_challenge = $4, profile_reply = $5, profile_fix = $6, skael_set = $7,
			agent_image = $8, mcp_servers = $9, feeds = $10, max_concurrent_rounds = $11, execution = $12, challenge_min_severity = $13,
			enabled_at = CASE WHEN $2 <> 'off' THEN coalesce(enabled_at, now()) ELSE enabled_at END
			WHERE lower(full_name) = lower($1) AND removed_at IS NULL`,
			r.FullName, r.Mode, r.Profiles.Review, r.Profiles.Challenge, r.Profiles.Reply, r.Profiles.Fix, r.SkaelSet, r.AgentImage,
			list(r.MCPServers), list(r.Feeds), r.MaxConcurrentRounds, r.Execution, r.ChallengeMinSeverity); err != nil {
			return fmt.Errorf("repository %s: %w", r.FullName, err)
		}
	}
	for _, t := range d.TeamRoles {
		if op("team_roles", t.Team) == "" {
			continue
		}
		if _, err := a.tx.Exec(ctx, `INSERT INTO team_roles (team_slug, role) VALUES ($1, $2) ON CONFLICT (team_slug) DO UPDATE SET role = $2`, t.Team, t.Role); err != nil {
			return fmt.Errorf("team role %s: %w", t.Team, err)
		}
	}
	removals := map[string]string{
		"providers":     `DELETE FROM providers WHERE name = $1`,
		"profiles":      `DELETE FROM profiles WHERE name = $1`,
		"mcp_servers":   `DELETE FROM mcp_servers WHERE name = $1`,
		"extensions":    `DELETE FROM extensions WHERE name = $1`,
		"package_feeds": `DELETE FROM package_feeds WHERE name = $1`,
		"stores":        `DELETE FROM stores WHERE name = $1`,
		"agent_images":  `DELETE FROM agent_images WHERE name = $1`,
		"team_roles":    `DELETE FROM team_roles WHERE team_slug = $1`,
	}
	for _, c := range changes {
		if c.Op != "remove" {
			continue
		}
		if _, err := a.tx.Exec(ctx, removals[c.Section], c.Name); err != nil {
			return fmt.Errorf("remove %s %s: %w", c.Section, c.Name, err)
		}
	}
	return nil
}

func storeDefaults(s []Store) []string {
	out := []string{}
	for _, x := range s {
		if x.Default {
			out = append(out, x.Name)
		}
	}
	return out
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
