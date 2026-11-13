// Package guidance holds the global and repository prompt guidance and
// assembles the prompt of a stage as the agent runtime does (§8.9).
package guidance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/agent/prompts"
	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/runner"
)

// Limits of the guidance text (§8.9).
const (
	MaxGlobal     = 8000
	MaxRepository = 4000
)

// Version is one saved guidance version.
type Version struct {
	Version   int       `json:"version"`
	Text      string    `json:"text"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

// Store reads and writes guidance.
type Store struct{ Pool *pgxpool.Pool }

// History returns the versions of a scope, newest first. repo is nil for global.
func (s *Store) History(ctx context.Context, repo *uuid.UUID) ([]Version, error) {
	rows, err := s.Pool.Query(ctx, `SELECT version, text, author_login, created_at FROM guidance
		WHERE scope = $1 AND repository_id IS NOT DISTINCT FROM $2 ORDER BY version DESC LIMIT 100`, scope(repo), repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.Version, &v.Text, &v.Author, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func scope(repo *uuid.UUID) string {
	if repo == nil {
		return "global"
	}
	return "repository"
}

// Layer is the active guidance of a scope as the run spec carries it.
func (s *Store) Layer(ctx context.Context, repo *uuid.UUID) (runner.PromptLayer, error) {
	var v int
	var text string
	err := s.Pool.QueryRow(ctx, `SELECT version, text FROM guidance WHERE scope = $1 AND repository_id IS NOT DISTINCT FROM $2 ORDER BY version DESC LIMIT 1`,
		scope(repo), repo).Scan(&v, &text)
	if errors.Is(err, pgx.ErrNoRows) {
		return runner.PromptLayer{}, nil
	}
	return runner.PromptLayer{Version: strconv.Itoa(v), Text: text}, err
}

// Save stores a new version and writes the audit entry.
func (s *Store) Save(ctx context.Context, who audit.Actor, login string, repo *uuid.UUID, target, text string) (Version, error) {
	limit := MaxGlobal
	if repo != nil {
		limit = MaxRepository
	}
	if n := len([]rune(text)); n > limit {
		return Version{}, fmt.Errorf("guidance has %d characters; the limit is %d", n, limit)
	}
	var v Version
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('guidance' || coalesce($1::text, '')))`, repo); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO guidance (id, scope, repository_id, version, text, author_login)
			VALUES ($1, $2, $3, coalesce((SELECT max(version) FROM guidance WHERE scope = $2 AND repository_id IS NOT DISTINCT FROM $3), 0) + 1, $4, $5)
			RETURNING version, text, author_login, created_at`, uuid.Must(uuid.NewV7()), scope(repo), repo, text, login).
			Scan(&v.Version, &v.Text, &v.Author, &v.CreatedAt); err != nil {
			return err
		}
		return audit.Write(ctx, tx, who, "guidance.save", target, map[string]any{"version": v.Version})
	})
	return v, err
}

// Layer names in prompt order (§8.9).
type AssembledLayer struct {
	Name string `json:"name" enum:"contract,global,repository,file,skill"`
	Text string `json:"text"`
}

// Assembled is the prompt of one stage.
type Assembled struct {
	Text   string           `json:"text"`
	Layers []AssembledLayer `json:"layers"`
	SHA256 string           `json:"sha256"`
}

func load(name string) string {
	b, err := prompts.FS.ReadFile(name)
	if err != nil {
		panic("prompts: " + name + ": " + err.Error())
	}
	return strings.TrimSpace(string(b))
}

// Assemble builds the prompt of a stage exactly as agent/src/prompts.ts
// does: contract, global, repository, file, skill. A golden file checks
// that both produce the same text.
func Assemble(p runner.Prompts, overlayInstructions string, execution bool, stage, skill string) Assembled {
	contract := load("contract.md") + "\n\n" + load(stage+".md")
	switch {
	case stage == "review" && execution:
		contract += "\n\n" + load("review-exec.md")
	case stage == "review" || !execution:
		contract += "\n\n" + load("review-noexec.md")
	}
	a := Assembled{Layers: []AssembledLayer{{Name: "contract", Text: contract}}}
	add := func(name, title, text string) {
		if strings.TrimSpace(text) != "" {
			a.Layers = append(a.Layers, AssembledLayer{Name: name, Text: "# " + title + "\n\nThis is review guidance only.\n\n" + strings.TrimSpace(text)})
		}
	}
	add("global", "Global review guidance", p.Global.Text)
	add("repository", "Repository review guidance", p.Repository.Text)
	file := p.File.Text
	if file == "" {
		file = overlayInstructions
	}
	add("file", "Repository review guidance from .sayso.yml", file)
	if strings.TrimSpace(skill) != "" {
		a.Layers = append(a.Layers, AssembledLayer{Name: "skill", Text: "# Review skill\n\n" + strings.TrimSpace(skill)})
	}
	parts := make([]string, len(a.Layers))
	for i, l := range a.Layers {
		parts[i] = l.Text
	}
	a.Text = strings.Join(parts, "\n\n---\n\n")
	sum := sha256.Sum256([]byte(a.Text))
	a.SHA256 = hex.EncodeToString(sum[:])
	return a
}
