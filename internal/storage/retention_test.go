package storage_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/storage"
)

func TestSCN_OBS_004_Retention(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	ctx := context.Background()
	run, _, err := runner.Create(ctx, e.Deps.Pool, runner.NewRun{Kind: "noop"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reg := &storage.Registry{Pool: e.Deps.Pool}
	sid, st, err := reg.Default(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.SQL(t, `UPDATE stores SET retention_days = 90`)
	old, fresh := time.Now().AddDate(0, 0, -91), time.Now().AddDate(0, 0, -10)
	for seq, at := range map[int64]time.Time{1: old, 2: fresh} {
		key := "runs/" + run.ID.String() + "/events/" + string(rune('0'+seq)) + ".json.gz"
		if err := st.Put(ctx, key, bytes.NewReader([]byte("{}")), 2); err != nil {
			t.Fatal(err)
		}
		e.SQL(t, `INSERT INTO run_events (run_id, seq, at, type, payload_key, store_id) VALUES ($1, $2, $3, 'note', $4, $5)`, run.ID, seq, at, key, sid)
	}
	if err := st.Put(ctx, "runs/old/artifacts/result.json", bytes.NewReader([]byte("{}")), 2); err != nil {
		t.Fatal(err)
	}
	e.SQL(t, `INSERT INTO artifacts (run_id, name, store_id, key, size, sha256, created_at) VALUES ($1, 'result.json', $2, 'runs/old/artifacts/result.json', 2, 'x', $3)`, run.ID, sid, old)
	// An index row older than the index retention goes too.
	e.SQL(t, `INSERT INTO run_events (run_id, seq, at, type) VALUES ($1, 3, now() - interval '401 days', 'note')`, run.ID)

	res, err := (&storage.Retention{Registry: reg}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Payloads != 1 || res.Artifacts != 1 || res.IndexRows != 1 {
		t.Fatalf("retention %+v", res)
	}
	if n := e.Count(t, `SELECT count(*) FROM run_events WHERE run_id = $1 AND seq = 1 AND payload_key IS NULL`, run.ID); n != 1 {
		t.Fatal("the old index row is gone or keeps its payload")
	}
	if n := e.Count(t, `SELECT count(*) FROM run_events WHERE run_id = $1 AND seq = 2 AND payload_key IS NOT NULL`, run.ID); n != 1 {
		t.Fatal("the fresh payload was removed")
	}
	if _, err := st.Get(ctx, "runs/"+run.ID.String()+"/events/1.json.gz"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("the old payload is still in the store: %v", err)
	}
	if n := e.Count(t, `SELECT count(*) FROM artifacts`); n != 0 {
		t.Fatal("the old artifact stays")
	}
}
