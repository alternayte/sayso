// Package secrets stores write-only secret values, encrypted with
// AES-256-GCM, or references to environment variables (D-15).
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
)

// Redactor learns each secret value so logs and stored text hide it.
type Redactor interface {
	Add(name, value string)
}

// Learn registers a value that is derived from a secret, for example a
// Basic credential, so stored text hides it too.
func (s *Store) Learn(name, value string) {
	if s.red != nil {
		s.red.Add(name, value)
	}
}

// LearnEnv registers the value of an environment variable that a secret
// refers to, after a caller stored the reference in its own transaction.
func (s *Store) LearnEnv(name, envName string) {
	if v := s.getenv(envName); v != "" {
		s.Learn(name, v)
	}
}

// ErrWrongKey means the configured key did not encrypt this value.
var ErrWrongKey = errors.New("secret was encrypted with another key: check SAYSO_SECRET_KEY")

// Store reads and writes secrets with one key.
type Store struct {
	pool    *pgxpool.Pool
	aead    cipher.AEAD
	version int32
	red     Redactor
	getenv  func(string) string
}

// Meta is what the API may show about a secret. It never holds the value.
type Meta struct {
	Name      string
	Kind      string
	Last4     string
	EnvName   string
	EnvSet    bool
	UpdatedBy string
	UpdatedAt time.Time
}

// Status is "set" or "not set" for an environment reference, else "stored".
func (m Meta) Status() string {
	if m.Kind == "env_ref" {
		if m.EnvSet {
			return "set"
		}
		return "not set"
	}
	return "stored"
}

// Open registers the key version of key and loads every stored value
// into the redactor.
func Open(ctx context.Context, pool *pgxpool.Pool, key []byte, red Redactor, getenv func(string) string) (*Store, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	s := &Store{pool: pool, aead: aead, red: red, getenv: getenv}
	if s.version, err = keyVersion(ctx, pool, key); err != nil {
		return nil, err
	}
	rows, err := New(pool).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if v, err := s.value(r); err == nil {
			red.Add(r.Name, v)
		}
	}
	return s, nil
}

func keyVersion(ctx context.Context, db DBTX, key []byte) (int32, error) {
	fp := fingerprint(key)
	q := New(db)
	v, err := q.KeyVersionByFingerprint(ctx, fp)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	max, err := q.MaxKeyVersion(ctx)
	if err != nil {
		return 0, err
	}
	if err := q.InsertKey(ctx, InsertKeyParams{Version: max + 1, Fingerprint: fp}); err != nil {
		return 0, err
	}
	return q.KeyVersionByFingerprint(ctx, fp)
}

func fingerprint(key []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("sayso secret key fingerprint"))
	return m.Sum(nil)
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func seal(aead cipher.AEAD, name, value string) (ct, nonce []byte, err error) {
	nonce = make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return aead.Seal(nil, nonce, []byte(value), []byte(name)), nonce, nil
}

// Put stores a value and writes an audit entry without the value.
func (s *Store) Put(ctx context.Context, by audit.Actor, name, value string) (Meta, error) {
	if value == "" {
		return Meta{}, fmt.Errorf("secret %s: empty value", name)
	}
	ct, nonce, err := seal(s.aead, name, value)
	if err != nil {
		return Meta{}, err
	}
	var row Secret
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		row, err = New(tx).UpsertStored(ctx, UpsertStoredParams{
			ID: id, Name: name, Ciphertext: ct, Nonce: nonce, KeyVersion: &s.version,
			Last4: last4(value), UpdatedBy: by.Login,
		})
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, by, "secrets.put", name, map[string]any{"kind": "stored"})
	})
	if err != nil {
		return Meta{}, err
	}
	s.red.Add(name, value)
	return s.meta(row), nil
}

// PutEnvRef stores a reference to an environment variable of the API process.
func (s *Store) PutEnvRef(ctx context.Context, by audit.Actor, name, envName string) (Meta, error) {
	if envName == "" {
		return Meta{}, fmt.Errorf("secret %s: empty variable name", name)
	}
	var row Secret
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		row, err = New(tx).UpsertEnvRef(ctx, UpsertEnvRefParams{ID: id, Name: name, EnvName: &envName, UpdatedBy: by.Login})
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, by, "secrets.put", name, map[string]any{"kind": "env_ref", "env": envName})
	})
	if err != nil {
		return Meta{}, err
	}
	if v := s.getenv(envName); v != "" {
		s.red.Add(name, v)
	}
	return s.meta(row), nil
}

func (s *Store) Meta(ctx context.Context, name string) (Meta, error) {
	row, err := New(s.pool).Get(ctx, name)
	if err != nil {
		return Meta{}, err
	}
	return s.meta(row), nil
}

// Value returns the plaintext. Only server code that calls an upstream
// service uses it; no API response carries it (SI-05).
func (s *Store) Value(ctx context.Context, name string) (string, error) {
	row, err := New(s.pool).Get(ctx, name)
	if err != nil {
		return "", fmt.Errorf("secret %s: %w", name, err)
	}
	return s.value(row)
}

// value decrypts or resolves a secret. Each read registers the value with
// the redactor, so a secret that another process stored (config apply from
// the CLI) is hidden from the first use.
func (s *Store) value(r Secret) (string, error) {
	v, err := s.plain(r)
	if err == nil {
		s.Learn(r.Name, v)
	}
	return v, err
}

func (s *Store) plain(r Secret) (string, error) {
	if r.Kind == "env_ref" {
		v := s.getenv(deref(r.EnvName))
		if v == "" {
			return "", fmt.Errorf("secret %s: environment variable %s is not set", r.Name, deref(r.EnvName))
		}
		return v, nil
	}
	if r.KeyVersion == nil || *r.KeyVersion != s.version {
		return "", ErrWrongKey
	}
	plain, err := s.aead.Open(nil, r.Nonce, r.Ciphertext, []byte(r.Name))
	if err != nil {
		return "", ErrWrongKey
	}
	return string(plain), nil
}

func (s *Store) meta(r Secret) Meta {
	m := Meta{Name: r.Name, Kind: r.Kind, Last4: r.Last4, UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt}
	if r.Kind == "env_ref" {
		m.EnvName = deref(r.EnvName)
		m.EnvSet = s.getenv(m.EnvName) != ""
	}
	return m
}

// Rekey decrypts every stored secret with oldKey and encrypts it with
// newKey in one transaction (REQ-CFG-009).
func Rekey(ctx context.Context, pool *pgxpool.Pool, oldKey, newKey []byte, by audit.Actor) error {
	oldAEAD, err := newAEAD(oldKey)
	if err != nil {
		return err
	}
	newAEADv, err := newAEAD(newKey)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		oldVer, err := keyVersion(ctx, tx, oldKey)
		if err != nil {
			return err
		}
		newVer, err := keyVersion(ctx, tx, newKey)
		if err != nil {
			return err
		}
		q := New(tx)
		rows, err := q.StoredForUpdate(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.KeyVersion == nil || *r.KeyVersion != oldVer {
				return fmt.Errorf("secret %s: %w", r.Name, ErrWrongKey)
			}
			plain, err := oldAEAD.Open(nil, r.Nonce, r.Ciphertext, []byte(r.Name))
			if err != nil {
				return fmt.Errorf("secret %s: %w", r.Name, ErrWrongKey)
			}
			ct, nonce, err := seal(newAEADv, r.Name, string(plain))
			if err != nil {
				return err
			}
			if err := q.Reseal(ctx, ResealParams{ID: r.ID, Ciphertext: ct, Nonce: nonce, KeyVersion: &newVer}); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, by, "secrets.rekey", "", map[string]any{"count": len(rows), "key_version": newVer})
	})
}

func last4(v string) string {
	r := []rune(v)
	if len(r) <= 4 {
		return ""
	}
	return string(r[len(r)-4:])
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ValueByID returns the plaintext of the secret with this ID.
func (s *Store) ValueByID(ctx context.Context, id [16]byte) (string, error) {
	row, err := New(s.pool).GetByID(ctx, uuid.UUID(id))
	if err != nil {
		return "", fmt.Errorf("secret %s: %w", uuid.UUID(id), err)
	}
	return s.value(row)
}

// ID returns the ID of a named secret.
func (s *Store) ID(ctx context.Context, name string) (uuid.UUID, error) {
	row, err := New(s.pool).Get(ctx, name)
	return row.ID, err
}

// PutValue stores a value as the system actor and returns its ID.
func (s *Store) PutValue(ctx context.Context, name, value string) ([16]byte, error) {
	if _, err := s.Put(ctx, audit.System, name, value); err != nil {
		return [16]byte{}, err
	}
	return s.ID(ctx, name)
}

// CheckAll decrypts every stored secret and returns how many there are. It
// fails when the configured key cannot open one of them.
func (s *Store) CheckAll(ctx context.Context) (int, error) {
	rows, err := New(s.pool).All(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		if r.Kind != "stored" {
			continue
		}
		if _, err := s.value(r); err != nil {
			return n, fmt.Errorf("secret %s: %w", r.Name, err)
		}
		n++
	}
	return n, nil
}
