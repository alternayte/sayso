// Package storage holds run payloads and artifacts in the configured
// store (D-12). The event index always stays in PostgreSQL.
package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
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
	// Secret reads the secret of a store; nil means no store has one.
	Secret func(ctx context.Context, id uuid.UUID) (string, error)

	mu     sync.Mutex
	opened map[uuid.UUID]opened
}

type opened struct {
	store Store
	// version changes when the store row changes.
	version string
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
	if row.Driver == "postgres" {
		return Postgres{Pool: r.Pool}, nil
	}
	version := string(row.Config)
	if row.SecretID != nil {
		version += row.SecretID.String()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if o, ok := r.opened[row.ID]; ok && o.version == version {
		return o.store, nil
	}
	secret := ""
	if row.SecretID != nil {
		if r.Secret == nil {
			return nil, errors.New("store " + row.Name + " has a secret, and the registry cannot read secrets")
		}
		var err error
		if secret, err = r.Secret(context.Background(), *row.SecretID); err != nil {
			return nil, err
		}
	}
	s, err := openDriver(row.Driver, row.Config, secret)
	if err != nil {
		return nil, err
	}
	if r.opened == nil {
		r.opened = map[uuid.UUID]opened{}
	}
	r.opened[row.ID] = opened{store: s, version: version}
	return s, nil
}

// Open opens a store from a config that is not saved yet, for the test button.
func Open(pool *pgxpool.Pool, driver string, config []byte, secret string) (Store, error) {
	if driver == "postgres" {
		return Postgres{Pool: pool}, nil
	}
	return openDriver(driver, config, secret)
}

// Object is one object of a batch put.
type Object struct {
	Key  string
	Data []byte
}

// PutAll stores a batch of objects. The postgres driver writes them in one
// round trip, inside tx when it is not nil, so the batch commits once;
// other drivers write up to 8 at a time.
func PutAll(ctx context.Context, s Store, tx pgx.Tx, objs []Object) error {
	if len(objs) == 0 {
		return nil
	}
	if p, ok := s.(Postgres); ok {
		b := &pgx.Batch{}
		for _, o := range objs {
			if len(o.Data) > MaxPostgresObject {
				return errors.New("object is larger than the postgres store allows")
			}
			b.Queue(`INSERT INTO blobs (key, data) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET data = EXCLUDED.data, created_at = now()`, o.Key, o.Data)
		}
		if tx != nil {
			return tx.SendBatch(ctx, b).Close()
		}
		return p.Pool.SendBatch(ctx, b).Close()
	}
	sem := make(chan struct{}, 8)
	errs := make(chan error, len(objs))
	for _, o := range objs {
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			errs <- s.Put(ctx, o.Key, bytes.NewReader(o.Data), int64(len(o.Data)))
		}()
	}
	var all []error
	for range objs {
		all = append(all, <-errs)
	}
	return errors.Join(all...)
}
