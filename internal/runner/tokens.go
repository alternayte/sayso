// Package runner holds agent runs, their run tokens, the runner protocol API
// and the process and Kubernetes runners.
package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TokenLifetime is the longest a run token works (SI-02).
const TokenLifetime = 2 * time.Hour

// ErrInvalidToken means the token is unknown, expired or of an ended run.
var ErrInvalidToken = errors.New("run token is not valid")

// Model is one provider and model that a run may call, with the prices of
// its profile target (per million tokens).
type Model struct {
	Profile        string   `json:"profile"`
	Provider       string   `json:"provider"`
	Model          string   `json:"model"`
	MaxTokens      int      `json:"max_tokens,omitempty"`
	ContextWindow  int      `json:"context_window,omitempty"`
	Reasoning      string   `json:"reasoning,omitempty"`
	Temperature    *float64 `json:"temperature,omitempty"`
	PriceIn        float64  `json:"price_in"`
	PriceOut       float64  `json:"price_out"`
	PriceCacheRead float64  `json:"price_cache_read"`
}

// NewRun is what the worker knows when it creates an agent run.
type NewRun struct {
	RoundID      *uuid.UUID
	ReviewID     *uuid.UUID
	RepositoryID *uuid.UUID
	Kind         string
	Runner       string
	Models       []Model
	BudgetUSD    float64
	Stages       []string
	Spec         Spec
}

// Create inserts a running agent run and returns it with its token. Only
// the SHA-256 of the token is stored.
func Create(ctx context.Context, db DBTX, n NewRun, now time.Time) (AgentRun, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return AgentRun{}, "", err
	}
	tok := "srt_" + base64.RawURLEncoding.EncodeToString(raw)
	id, err := uuid.NewV7()
	if err != nil {
		return AgentRun{}, "", err
	}
	models, err := json.Marshal(n.Models)
	if err != nil {
		return AgentRun{}, "", err
	}
	stages, err := json.Marshal(n.Stages)
	if err != nil {
		return AgentRun{}, "", err
	}
	spec, err := json.Marshal(n.Spec)
	if err != nil {
		return AgentRun{}, "", err
	}
	if n.Runner == "" {
		n.Runner = "process"
	}
	exp := now.Add(TokenLifetime)
	run, err := New(db).CreateRun(ctx, CreateRunParams{
		ID: id, RoundID: n.RoundID, ReviewID: n.ReviewID, RepositoryID: n.RepositoryID, Kind: n.Kind, Runner: n.Runner,
		TokenHash: hash(tok), TokenExpiresAt: &exp, Models: models, BudgetUsd: n.BudgetUSD, Stages: stages, Spec: spec,
	})
	return run, tok, err
}

func hash(tok string) []byte {
	s := sha256.Sum256([]byte(tok))
	return s[:]
}

// Active returns the run of a token when the run is running and the token
// has not expired.
func Active(ctx context.Context, db DBTX, tok string, now time.Time) (AgentRun, error) {
	if tok == "" {
		return AgentRun{}, ErrInvalidToken
	}
	run, err := New(db).RunByTokenHash(ctx, hash(tok))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentRun{}, ErrInvalidToken
	}
	if err != nil {
		return AgentRun{}, err
	}
	if run.Status != "running" || run.TokenExpiresAt == nil || !now.Before(*run.TokenExpiresAt) {
		return AgentRun{}, ErrInvalidToken
	}
	return run, nil
}

// AllowedModels decodes the model snapshot of a run.
func AllowedModels(run AgentRun) ([]Model, error) {
	var ms []Model
	err := json.Unmarshal(run.Models, &ms)
	return ms, err
}

// TokenFrom reads a run token from the request headers that model APIs use
// for keys: Authorization bearer, x-api-key, api-key or x-goog-api-key.
func TokenFrom(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	for _, name := range []string{"x-api-key", "api-key", "x-goog-api-key"} {
		if v := r.Header.Get(name); v != "" {
			return v
		}
	}
	return r.URL.Query().Get("key")
}
