package app

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrationFS() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory
	}
	return sub
}

// MigrationCount returns the number of embedded migrations.
func MigrationCount() (int, error) {
	entries, err := fs.ReadDir(migrationFS(), ".")
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

func newMigrator(db *sql.DB, locked bool) (*goose.Provider, error) {
	opts := []goose.ProviderOption{goose.WithDisableGlobalRegistry(true)}
	if locked {
		locker, err := lock.NewPostgresSessionLocker()
		if err != nil {
			return nil, err
		}
		opts = append(opts, goose.WithSessionLocker(locker))
	}
	return goose.NewProvider(goose.DialectPostgres, db, migrationFS(), opts...)
}

// Migrate applies pending migrations under a PostgreSQL advisory lock, so
// several processes that start at once apply each migration once.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := newMigrator(db, true)
	if err != nil {
		return 0, fmt.Errorf("migrations: %w", err)
	}
	res, err := p.Up(ctx)
	if err != nil {
		return 0, fmt.Errorf("apply migrations: %w", err)
	}
	return len(res), nil
}

// MigrationsCurrent reports whether the database has no pending migration.
func MigrationsCurrent(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := newMigrator(db, false)
	if err != nil {
		return false, err
	}
	pending, err := p.HasPending(ctx)
	return !pending, err
}

// RunMigrate connects to the database and applies pending migrations.
func RunMigrate(ctx context.Context, url string, log interface{ Info(string, ...any) }) error {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	n, err := Migrate(ctx, pool)
	if err != nil {
		return err
	}
	log.Info("migrations applied", "count", n)
	return nil
}
