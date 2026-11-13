// Package settings stores versioned operational settings. Each change keeps
// the author, the time and the old and new values (REQ-CFG-001).
package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
)

const (
	DebounceSeconds          = "debounce_seconds"
	ReconcileIntervalMinutes = "reconcile_interval_minutes"
	WorkerMaxAgentRuns       = "worker_max_agent_runs"
	MaxConcurrentRounds      = "max_concurrent_rounds"
	RoundTimeoutMinutes      = "round_timeout_minutes"
	ReplyTimeoutMinutes      = "reply_timeout_minutes"
	SetupTimeoutMinutes      = "setup_timeout_minutes"
	MaxFindingsPerRound      = "max_findings_per_round"
	MaxToolCallsPerStage     = "max_tool_calls_per_stage"
	MaxInlineComments        = "max_inline_comments"
	MatchSimilarity          = "match_similarity"
	MonthlyBudgetUSD         = "monthly_budget_usd"
	OverrideTeams            = "override_teams"
	AllowedProfiles          = "allowed_profiles"
	EventIndexRetentionDays  = "event_index_retention_days"
	RegisterTemporalNS       = "register_temporal_namespace"
	GitHubWebURL             = "github_web_url"
	GitHubAPIURL             = "github_api_url"
	SkaelURL                 = "skael_url"
	SkaelDefaultSet          = "skael_default_set"
	SkaelAllowedSets         = "skael_allowed_sets"
	SkaelReviewSkill         = "skael_review_skill"
	SkaelActivationHook      = "skael_activation_hook"
)

// Defaults holds every known key with its default value. The JSON type of
// the default is the type that the key accepts.
var Defaults = map[string]json.RawMessage{
	DebounceSeconds:          json.RawMessage(`90`),
	ReconcileIntervalMinutes: json.RawMessage(`10`),
	WorkerMaxAgentRuns:       json.RawMessage(`4`),
	MaxConcurrentRounds:      json.RawMessage(`2`),
	RoundTimeoutMinutes:      json.RawMessage(`30`),
	ReplyTimeoutMinutes:      json.RawMessage(`5`),
	SetupTimeoutMinutes:      json.RawMessage(`10`),
	MaxFindingsPerRound:      json.RawMessage(`25`),
	MaxToolCallsPerStage:     json.RawMessage(`200`),
	MaxInlineComments:        json.RawMessage(`10`),
	MatchSimilarity:          json.RawMessage(`0.6`),
	MonthlyBudgetUSD:         json.RawMessage(`0`),
	OverrideTeams:            json.RawMessage(`[]`),
	AllowedProfiles:          json.RawMessage(`["review","challenge","reply","fix"]`),
	EventIndexRetentionDays:  json.RawMessage(`400`),
	RegisterTemporalNS:       json.RawMessage(`true`),
	GitHubWebURL:             json.RawMessage(`"https://github.com"`),
	GitHubAPIURL:             json.RawMessage(`"https://api.github.com"`),
	SkaelURL:                 json.RawMessage(`""`),
	SkaelDefaultSet:          json.RawMessage(`""`),
	SkaelAllowedSets:         json.RawMessage(`[]`),
	SkaelReviewSkill:         json.RawMessage(`"pr-review"`),
	SkaelActivationHook:      json.RawMessage(`true`),
}

// ErrUnknownKey is returned for a key that is not in Defaults.
var ErrUnknownKey = errors.New("unknown setting")

// Store reads and changes settings.
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// SeedDefaults inserts revision 1 of each key that has no row yet.
func (s *Store) SeedDefaults(ctx context.Context) error {
	keys := make([]string, 0, len(Defaults))
	for k := range Defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := New(tx)
		for _, k := range keys {
			if err := q.SeedDefault(ctx, SeedDefaultParams{Key: k, Value: Defaults[k]}); err != nil {
				return fmt.Errorf("seed %s: %w", k, err)
			}
			if err := q.SeedHistory(ctx, k); err != nil {
				return fmt.Errorf("seed history %s: %w", k, err)
			}
		}
		return nil
	})
}

func (s *Store) Get(ctx context.Context, key string) (Setting, error) {
	return New(s.pool).Get(ctx, key)
}

func (s *Store) All(ctx context.Context) ([]Setting, error) {
	return New(s.pool).All(ctx)
}

func (s *Store) History(ctx context.Context, key string) ([]SettingsHistory, error) {
	return New(s.pool).History(ctx, key)
}

// Int reads an integer setting, or its default when the row is missing.
func (s *Store) Int(ctx context.Context, key string) (int, error) {
	raw := Defaults[key]
	if row, err := s.Get(ctx, key); err == nil {
		raw = row.Value
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("setting %s: %w", key, err)
	}
	return n, nil
}

// String reads a string setting, or its default when the row is missing.
func (s *Store) String(ctx context.Context, key string) (string, error) {
	raw := Defaults[key]
	if row, err := s.Get(ctx, key); err == nil {
		raw = row.Value
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("setting %s: %w", key, err)
	}
	return v, nil
}

// Set validates and stores a new value, writes the history row and an
// audit entry in one transaction, and returns the new row.
func (s *Store) Set(ctx context.Context, by audit.Actor, key string, value json.RawMessage) (Setting, error) {
	var out Setting
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = SetTx(ctx, tx, by, key, value)
		return err
	})
	return out, err
}

// SetTx is Set inside the caller's transaction, for config apply.
func SetTx(ctx context.Context, tx pgx.Tx, by audit.Actor, key string, value json.RawMessage) (Setting, error) {
	def, ok := Defaults[key]
	if !ok {
		return Setting{}, fmt.Errorf("%w: %s", ErrUnknownKey, key)
	}
	value, err := normalize(value)
	if err != nil {
		return Setting{}, fmt.Errorf("setting %s: %w", key, err)
	}
	if kind(value) != kind(def) {
		return Setting{}, fmt.Errorf("setting %s takes a %s, not a %s", key, kind(def), kind(value))
	}
	q := New(tx)
	old, err := q.GetForUpdate(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := q.SeedDefault(ctx, SeedDefaultParams{Key: key, Value: def}); err != nil {
			return Setting{}, err
		}
		if err := q.SeedHistory(ctx, key); err != nil {
			return Setting{}, err
		}
		old, err = q.GetForUpdate(ctx, key)
	}
	if err != nil {
		return Setting{}, err
	}
	out, err := q.Update(ctx, UpdateParams{Key: key, Value: value, UpdatedBy: by.Login})
	if err != nil {
		return Setting{}, err
	}
	if err := q.InsertHistory(ctx, InsertHistoryParams{
		Key: key, Revision: out.Revision, OldValue: old.Value, NewValue: value,
		UpdatedBy: by.Login, UpdatedAt: out.UpdatedAt,
	}); err != nil {
		return Setting{}, err
	}
	return out, audit.Write(ctx, tx, by, "settings.update", key, map[string]any{
		"old": json.RawMessage(old.Value), "new": json.RawMessage(value), "revision": out.Revision,
	})
}

func normalize(v json.RawMessage) (json.RawMessage, error) {
	var x any
	d := json.NewDecoder(bytes.NewReader(v))
	d.UseNumber()
	if err := d.Decode(&x); err != nil {
		return nil, fmt.Errorf("value is not JSON: %w", err)
	}
	return json.Marshal(x)
}

func kind(v json.RawMessage) string {
	switch b := bytes.TrimSpace(v); {
	case len(b) == 0:
		return "empty value"
	case b[0] == '"':
		return "string"
	case b[0] == '[':
		return "list"
	case b[0] == '{':
		return "object"
	case b[0] == 't' || b[0] == 'f':
		return "boolean"
	case b[0] == 'n':
		return "null"
	default:
		return "number"
	}
}

// Decode reads a setting of any JSON type into out, or its default when the
// row is missing.
func (s *Store) Decode(ctx context.Context, key string, out any) error {
	raw := Defaults[key]
	if row, err := s.Get(ctx, key); err == nil {
		raw = row.Value
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("setting %s: %w", key, err)
	}
	return nil
}
