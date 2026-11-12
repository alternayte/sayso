package settings_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/settings"
	"github.com/alternayte/sayso/internal/testenv"
)

func TestSCN_CFG_001_SettingHistory(t *testing.T) {
	ctx := context.Background()
	pool := testenv.DB(t)
	s := settings.NewStore(pool)
	if err := s.SeedDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	alice := audit.User(uuid.New(), "octo-admin")
	bob := audit.User(uuid.New(), "dev-alice")
	if _, err := s.Set(ctx, alice, settings.DebounceSeconds, json.RawMessage(`60`)); err != nil {
		t.Fatal(err)
	}
	cur, err := s.Set(ctx, bob, settings.DebounceSeconds, json.RawMessage(`120`))
	if err != nil {
		t.Fatal(err)
	}
	if cur.Revision != 3 {
		t.Fatalf("revision = %d, want 3", cur.Revision)
	}
	h, err := s.History(ctx, settings.DebounceSeconds)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		rev          int32
		by, old, new string
	}
	want := []row{{1, "system", "", "90"}, {2, "octo-admin", "90", "60"}, {3, "dev-alice", "60", "120"}}
	if len(h) != len(want) {
		t.Fatalf("history has %d rows, want %d", len(h), len(want))
	}
	for i, w := range want {
		got := row{h[i].Revision, h[i].UpdatedBy, string(h[i].OldValue), string(h[i].NewValue)}
		if got != w {
			t.Errorf("history[%d] = %+v, want %+v", i, got, w)
		}
		if h[i].UpdatedAt.IsZero() {
			t.Errorf("history[%d] has no time", i)
		}
	}
	entries, err := audit.Search(ctx, pool, audit.Query{Action: "settings.update"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("audit has %d settings.update rows, want 2", len(entries))
	}
	if _, err := s.Set(ctx, alice, settings.DebounceSeconds, json.RawMessage(`"fast"`)); err == nil {
		t.Fatal("a string for a number setting was accepted")
	}
	if _, err := s.Set(ctx, alice, "no_such_key", json.RawMessage(`1`)); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}
