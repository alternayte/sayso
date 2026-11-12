package secrets_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/secrets"
	"github.com/alternayte/sayso/internal/testenv"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestSCN_CFG_002_SecretsWriteOnly(t *testing.T) {
	ctx := context.Background()
	pool := testenv.DB(t)
	red := app.NewRedactor()
	env := map[string]string{"SET_VAR": "from-env-value"}
	s, err := secrets.Open(ctx, pool, key(1), red, func(n string) string { return env[n] })
	if err != nil {
		t.Fatal(err)
	}
	const plain = "sk-test-provider-key-0042"
	meta, err := s.Put(ctx, audit.System, "provider.acme-openai", plain)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Last4 != "0042" || meta.Kind != "stored" || meta.UpdatedAt.IsZero() {
		t.Fatalf("meta = %+v", meta)
	}
	var cipher []byte
	if err := pool.QueryRow(ctx, `SELECT ciphertext FROM secrets WHERE name = $1`, "provider.acme-openai").Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cipher, []byte(plain)) || bytes.Contains([]byte(base64.StdEncoding.EncodeToString(cipher)), []byte(plain)) {
		t.Fatal("the database value contains the plaintext")
	}
	got, err := s.Value(ctx, "provider.acme-openai")
	if err != nil || got != plain {
		t.Fatalf("Value = %q, %v", got, err)
	}
	if red.Redact("key="+plain) != "key=[redacted:provider.acme-openai]" {
		t.Fatalf("the redactor does not know the value: %s", red.Redact(plain))
	}

	if _, err := s.PutEnvRef(ctx, audit.System, "provider.unset", "UNSET_VAR"); err != nil {
		t.Fatal(err)
	}
	m, err := s.Meta(ctx, "provider.unset")
	if err != nil {
		t.Fatal(err)
	}
	if m.EnvSet || m.Status() != "not set" {
		t.Fatalf("unset env ref: %+v status %q", m, m.Status())
	}
	if _, err := s.PutEnvRef(ctx, audit.System, "provider.set", "SET_VAR"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Value(ctx, "provider.set"); err != nil || v != "from-env-value" {
		t.Fatalf("env ref value = %q, %v", v, err)
	}
	entries, err := audit.Search(ctx, pool, audit.Query{Action: "secrets.put"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if bytes.Contains(e.Details, []byte(plain)) {
			t.Fatal("the audit entry contains the value")
		}
	}
	if len(entries) != 3 {
		t.Fatalf("audit has %d secrets.put rows, want 3", len(entries))
	}
}

func TestSCN_CFG_009_Rekey(t *testing.T) {
	ctx := context.Background()
	pool := testenv.DB(t)
	getenv := func(string) string { return "" }
	a, err := secrets.Open(ctx, pool, key(1), app.NewRedactor(), getenv)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"one", "two", "three"} {
		if _, err := a.Put(ctx, audit.System, n, "value-of-"+n); err != nil {
			t.Fatal(err)
		}
	}
	if err := secrets.Rekey(ctx, pool, key(1), key(2), audit.System); err != nil {
		t.Fatal(err)
	}
	b, err := secrets.Open(ctx, pool, key(2), app.NewRedactor(), getenv)
	if err != nil {
		t.Fatal(err)
	}
	old, err := secrets.Open(ctx, pool, key(1), app.NewRedactor(), getenv)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"one", "two", "three"} {
		if v, err := b.Value(ctx, n); err != nil || v != "value-of-"+n {
			t.Fatalf("new key: %s = %q, %v", n, v, err)
		}
		if _, err := old.Value(ctx, n); err == nil {
			t.Fatalf("old key still decrypts %s", n)
		}
	}
}
