package secrets

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/audit"
)

// Input is a secret field in a request: a new value, or the name of an
// environment variable. An empty input keeps the stored secret.
type Input struct {
	Value string `json:"value,omitempty" doc:"New value. The API never returns it."`
	Env   string `json:"env,omitempty" doc:"Name of an environment variable of the API process."`
}

// Field is what a response shows of a secret field (REQ-CFG-002). It never
// holds the value.
type Field struct {
	Set       bool       `json:"set"`
	Kind      string     `json:"kind,omitempty" enum:"stored,env_ref"`
	Last4     string     `json:"last4,omitempty"`
	EnvName   string     `json:"env_name,omitempty"`
	Status    string     `json:"status,omitempty" enum:"stored,set,not set"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// Apply stores the input under name and returns the secret ID. With an
// empty input it returns current, unchanged.
func (s *Store) Apply(ctx context.Context, by audit.Actor, name string, in *Input, current *uuid.UUID) (*uuid.UUID, error) {
	if in == nil || (in.Value == "" && in.Env == "") {
		return current, nil
	}
	if in.Value != "" && in.Env != "" {
		return nil, fmt.Errorf("%s: give a value or an environment variable, not both", name)
	}
	var err error
	if in.Value != "" {
		_, err = s.Put(ctx, by, name, in.Value)
	} else {
		_, err = s.PutEnvRef(ctx, by, name, in.Env)
	}
	if err != nil {
		return nil, err
	}
	id, err := s.ID(ctx, name)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// Describe returns the display form of a secret field.
func (s *Store) Describe(ctx context.Context, id *uuid.UUID) (Field, error) {
	if id == nil {
		return Field{}, nil
	}
	row, err := New(s.pool).GetByID(ctx, *id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Field{}, nil
	}
	if err != nil {
		return Field{}, err
	}
	m := s.meta(row)
	at := m.UpdatedAt
	return Field{Set: true, Kind: m.Kind, Last4: m.Last4, EnvName: m.EnvName, Status: m.Status(), UpdatedAt: &at}, nil
}

// ValueOf returns the plaintext of a secret by ID, for server calls only.
func (s *Store) ValueOf(ctx context.Context, id *uuid.UUID) (string, error) {
	if id == nil {
		return "", nil
	}
	return s.ValueByID(ctx, *id)
}

// Remove deletes a secret and writes an audit entry.
func (s *Store) Remove(ctx context.Context, by audit.Actor, name string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := New(tx).Delete(ctx, name); err != nil {
			return err
		}
		return audit.Write(ctx, tx, by, "secrets.delete", name, nil)
	})
}
