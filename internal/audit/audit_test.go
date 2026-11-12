package audit_test

import (
	"context"
	"testing"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/testenv"
)

func TestSI_13_AuditAppendOnly(t *testing.T) {
	ctx := context.Background()
	pool := testenv.DB(t)
	for _, a := range []string{"settings.update", "secrets.put", "settings.update"} {
		if err := audit.Write(ctx, pool, audit.System, a, "x", nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []string{
		`UPDATE audit_log SET action = 'changed'`,
		`DELETE FROM audit_log`,
		`TRUNCATE audit_log`,
	} {
		if _, err := pool.Exec(ctx, stmt); err == nil {
			t.Fatalf("%s succeeded", stmt)
		}
	}
	got, err := audit.Search(ctx, pool, audit.Query{Action: "settings.update"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID < got[1].ID {
		t.Fatalf("filter returned %d rows, newest first: %v", len(got), got)
	}
}
