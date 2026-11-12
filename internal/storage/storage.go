// Package storage holds run payloads and artifacts in the configured
// store (D-12). The event index always stays in PostgreSQL.
package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound means the object does not exist.
var ErrNotFound = errors.New("object not found")

// Info describes a stored object.
type Info struct {
	Size    int64
	ModTime time.Time
}

// Store is one object store (§15.2).
type Store interface {
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Stat(ctx context.Context, key string) (Info, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

// Postgres keeps objects in the blobs table.
type Postgres struct{ Pool *pgxpool.Pool }

// MaxPostgresObject is the largest object the postgres driver accepts.
const MaxPostgresObject = 256 << 20

func (p Postgres) Put(ctx context.Context, key string, r io.Reader, _ int64) error {
	b, err := io.ReadAll(io.LimitReader(r, MaxPostgresObject+1))
	if err != nil {
		return err
	}
	if len(b) > MaxPostgresObject {
		return errors.New("object is larger than the postgres store allows")
	}
	return New(p.Pool).PutBlob(ctx, PutBlobParams{Key: key, Data: b})
}

func (p Postgres) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	b, err := New(p.Pool).GetBlob(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (p Postgres) Stat(ctx context.Context, key string) (Info, error) {
	row, err := New(p.Pool).StatBlob(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return Info{}, ErrNotFound
	}
	if err != nil {
		return Info{}, err
	}
	return Info{Size: row.Size, ModTime: row.CreatedAt}, nil
}

func (p Postgres) Delete(ctx context.Context, key string) error {
	return New(p.Pool).DeleteBlob(ctx, key)
}

func (p Postgres) List(ctx context.Context, prefix string) ([]string, error) {
	return New(p.Pool).ListBlobs(ctx, prefix)
}

// Registry opens the configured stores.
type Registry struct {
	Pool *pgxpool.Pool
}

// Default returns the default store and its ID. New objects go there.
func (r *Registry) Default(ctx context.Context) (uuid.UUID, Store, error) {
	row, err := New(r.Pool).DefaultStore(ctx)
	if err != nil {
		return uuid.UUID{}, nil, err
	}
	s, err := r.open(row)
	return row.ID, s, err
}

// ByID returns the store that holds an older object.
func (r *Registry) ByID(ctx context.Context, id uuid.UUID) (Store, error) {
	row, err := New(r.Pool).StoreByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.open(row)
}

func (r *Registry) open(row StoreRow) (Store, error) {
	switch row.Driver {
	case "postgres":
		return Postgres{Pool: r.Pool}, nil
	}
	return nil, errors.New("store driver " + row.Driver + " is not available")
}
