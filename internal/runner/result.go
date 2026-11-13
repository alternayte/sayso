package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/storage"
)

// ErrNoResult means the agent run stored no result.json.
var ErrNoResult = errors.New("the agent run stored no result.json")

// ReadResult reads result.json of an agent run from its store.
func ReadResult(ctx context.Context, pool *pgxpool.Pool, stores *storage.Registry, runID uuid.UUID) (*Result, error) {
	var storeID uuid.UUID
	var key string
	err := pool.QueryRow(ctx, `SELECT store_id, key FROM artifacts WHERE run_id = $1 AND name = 'result.json'`, runID).Scan(&storeID, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoResult
	}
	if err != nil {
		return nil, err
	}
	st, err := stores.ByID(ctx, storeID)
	if err != nil {
		return nil, err
	}
	rc, err := st.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	var res Result
	if err := json.NewDecoder(rc).Decode(&res); err != nil {
		return nil, fmt.Errorf("result.json: %w", err)
	}
	return &res, nil
}
