package runner

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/storage"
)

// RunsAPI is the run page API of the UI (§15.3).
type RunsAPI struct {
	Pool   *pgxpool.Pool
	Stores *storage.Registry
	// Poll is the stream poll interval (default 500 ms).
	Poll time.Duration
}

type RunBody struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Status     string         `json:"status"`
	ExitReason string         `json:"exit_reason"`
	RoundID    string         `json:"round_id,omitempty"`
	Repository string         `json:"repository,omitempty"`
	Number     int            `json:"number,omitempty"`
	HeadSHA    string         `json:"head_sha,omitempty"`
	TokensIn   int64          `json:"tokens_in"`
	TokensOut  int64          `json:"tokens_out"`
	CostUSD    float64        `json:"cost_usd"`
	Events     int            `json:"events"`
	Artifacts  []ArtifactBody `json:"artifacts"`
	CreatedAt  time.Time      `json:"created_at"`
	StartedAt  *time.Time     `json:"started_at"`
	FinishedAt *time.Time     `json:"finished_at"`
}

type ArtifactBody struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type EventBody struct {
	Seq        int64     `json:"seq"`
	At         time.Time `json:"at"`
	Stage      string    `json:"stage"`
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	DurationMS int32     `json:"duration_ms"`
	TokensIn   int64     `json:"tokens_in"`
	TokensOut  int64     `json:"tokens_out"`
	Preview    string    `json:"preview"`
	HasPayload bool      `json:"has_payload"`
}

const eventCols = `seq, at, stage, type, name, status, duration_ms, tokens_in, tokens_out, preview, payload_key IS NOT NULL`

func (a *RunsAPI) events(ctx context.Context, run uuid.UUID, after int64, limit int) ([]EventBody, error) {
	rows, err := a.Pool.Query(ctx, `SELECT `+eventCols+` FROM run_events WHERE run_id = $1 AND seq > $2 ORDER BY at, seq LIMIT $3`, run, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventBody{}
	for rows.Next() {
		var e EventBody
		if err := rows.Scan(&e.Seq, &e.At, &e.Stage, &e.Type, &e.Name, &e.Status, &e.DurationMS, &e.TokensIn, &e.TokensOut, &e.Preview, &e.HasPayload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func runID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return id, huma.Error404NotFound("run not found")
	}
	return id, nil
}

// Register adds the run operations for signed-in people.
func (a *RunsAPI) Register(api huma.API, view map[string]any) {
	tags := []string{"runs"}
	huma.Register(api, huma.Operation{OperationID: "get-run", Method: http.MethodGet, Path: "/runs/{id}", Tags: tags, Metadata: view},
		func(ctx context.Context, in *struct {
			ID string `path:"id" format:"uuid"`
		}) (*struct{ Body RunBody }, error) {
			id, err := runID(in.ID)
			if err != nil {
				return nil, err
			}
			var b RunBody
			var round *uuid.UUID
			err = a.Pool.QueryRow(ctx, `SELECT ar.id::text, ar.kind, ar.status, ar.exit_reason, ar.round_id, coalesce(rp.full_name, ''), coalesce(rv.pr_number, 0),
					coalesce(ro.head_sha, ''), ar.tokens_in, ar.tokens_out, ar.cost_usd::float8, (SELECT count(*) FROM run_events WHERE run_id = ar.id), ar.created_at, ar.started_at, ar.finished_at
				FROM agent_runs ar LEFT JOIN repositories rp ON rp.id = ar.repository_id LEFT JOIN reviews rv ON rv.id = ar.review_id LEFT JOIN rounds ro ON ro.id = ar.round_id
				WHERE ar.id = $1`, id).Scan(&b.ID, &b.Kind, &b.Status, &b.ExitReason, &round, &b.Repository, &b.Number, &b.HeadSHA, &b.TokensIn, &b.TokensOut, &b.CostUSD,
				&b.Events, &b.CreatedAt, &b.StartedAt, &b.FinishedAt)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("run not found")
			}
			if err != nil {
				return nil, err
			}
			if round != nil {
				b.RoundID = round.String()
			}
			rows, err := a.Pool.Query(ctx, `SELECT name, size FROM artifacts WHERE run_id = $1 ORDER BY name`, id)
			if err != nil {
				return nil, err
			}
			b.Artifacts, err = pgx.CollectRows(rows, pgx.RowToStructByPos[ArtifactBody])
			if b.Artifacts == nil {
				b.Artifacts = []ArtifactBody{}
			}
			return &struct{ Body RunBody }{b}, err
		})

	huma.Register(api, huma.Operation{OperationID: "list-run-events", Method: http.MethodGet, Path: "/runs/{id}/events", Tags: tags, Metadata: view},
		func(ctx context.Context, in *struct {
			ID    string `path:"id" format:"uuid"`
			After int64  `query:"after" doc:"Return events after this seq"`
			Limit int    `query:"limit" minimum:"1" maximum:"10000" default:"10000"`
		}) (*struct{ Body []EventBody }, error) {
			id, err := runID(in.ID)
			if err != nil {
				return nil, err
			}
			ev, err := a.events(ctx, id, in.After, in.Limit)
			return &struct{ Body []EventBody }{ev}, err
		})

	huma.Register(api, huma.Operation{OperationID: "get-run-event-payload", Method: http.MethodGet, Path: "/runs/{id}/events/{seq}/payload", Tags: tags, Metadata: view},
		func(ctx context.Context, in *struct {
			ID  string `path:"id" format:"uuid"`
			Seq int64  `path:"seq"`
		}) (*huma.StreamResponse, error) {
			id, err := runID(in.ID)
			if err != nil {
				return nil, err
			}
			var key string
			var store uuid.UUID
			err = a.Pool.QueryRow(ctx, `SELECT payload_key, store_id FROM run_events WHERE run_id = $1 AND seq = $2 AND payload_key IS NOT NULL`, id, in.Seq).Scan(&key, &store)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("the event has no payload; retention may have removed it")
			}
			if err != nil {
				return nil, err
			}
			return a.serve(ctx, store, key, "application/json", true)
		})

	huma.Register(api, huma.Operation{OperationID: "get-run-artifact", Method: http.MethodGet, Path: "/runs/{id}/artifacts/{name}", Tags: tags, Metadata: view},
		func(ctx context.Context, in *struct {
			ID   string `path:"id" format:"uuid"`
			Name string `path:"name" doc:"session.jsonl.gz, result.json or manifest.json"`
		}) (*huma.StreamResponse, error) {
			id, err := runID(in.ID)
			if err != nil {
				return nil, err
			}
			var key, ct string
			var store uuid.UUID
			err = a.Pool.QueryRow(ctx, `SELECT key, store_id, content_type FROM artifacts WHERE run_id = $1 AND name = $2`, id, in.Name).Scan(&key, &store, &ct)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("artifact not found")
			}
			if err != nil {
				return nil, err
			}
			return a.serve(ctx, store, key, ct, false)
		})

	sse.Register(api, huma.Operation{OperationID: "stream-run", Method: http.MethodGet, Path: "/runs/{id}/stream", Tags: tags, Metadata: view},
		map[string]any{"event": EventBody{}, "end": RunEnd{}},
		func(ctx context.Context, in *struct {
			ID    string `path:"id" format:"uuid"`
			After int64  `query:"after"`
		}, send sse.Sender) {
			id, err := uuid.Parse(in.ID)
			if err != nil {
				return
			}
			poll := a.Poll
			if poll == 0 {
				poll = 500 * time.Millisecond
			}
			// Server events have high seq numbers, so the stream follows time
			// order and skips the events it already sent.
			seen := map[int64]bool{}
			since := time.Time{}
			t := time.NewTicker(poll)
			defer t.Stop()
			for {
				rows, err := a.Pool.Query(ctx, `SELECT `+eventCols+` FROM run_events WHERE run_id = $1 AND at >= $2 ORDER BY at, seq LIMIT 2000`, id, since)
				if err != nil {
					return
				}
				var ev []EventBody
				for rows.Next() {
					var e EventBody
					if err := rows.Scan(&e.Seq, &e.At, &e.Stage, &e.Type, &e.Name, &e.Status, &e.DurationMS, &e.TokensIn, &e.TokensOut, &e.Preview, &e.HasPayload); err != nil {
						rows.Close()
						return
					}
					ev = append(ev, e)
				}
				rows.Close()
				fresh := 0
				for _, e := range ev {
					if seen[e.Seq] || e.Seq <= in.After {
						continue
					}
					if err := send.Data(e); err != nil {
						return
					}
					seen[e.Seq] = true
					fresh++
					// Keep a second of overlap: events can arrive with older times.
					if at := e.At.Add(-time.Second); at.After(since) {
						since = at
					}
				}
				var status string
				if err := a.Pool.QueryRow(ctx, `SELECT status FROM agent_runs WHERE id = $1`, id).Scan(&status); err != nil {
					return
				}
				if status != "queued" && status != "running" && fresh == 0 {
					_ = send.Data(RunEnd{Status: status})
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		})
}

// RunEnd closes a run stream.
type RunEnd struct {
	Status string `json:"status"`
}

func (a *RunsAPI) serve(ctx context.Context, storeID uuid.UUID, key, contentType string, gunzip bool) (*huma.StreamResponse, error) {
	st, err := a.Stores.ByID(ctx, storeID)
	if err != nil {
		return nil, err
	}
	rc, err := st.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, huma.Error404NotFound("the object is gone; retention may have removed it")
	}
	if err != nil {
		return nil, err
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer func() { _ = rc.Close() }()
		var r io.Reader = rc
		if gunzip {
			if gz, err := gzip.NewReader(rc); err == nil {
				defer func() { _ = gz.Close() }()
				r = gz
			}
		}
		hctx.SetHeader("Content-Type", contentType)
		hctx.SetHeader("Cache-Control", "private, no-store")
		_, _ = io.Copy(hctx.BodyWriter(), r)
	}}, nil
}
