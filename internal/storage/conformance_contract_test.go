//go:build contract

package storage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/azure/azurite"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/alternayte/sayso/internal/app"
	"github.com/alternayte/sayso/internal/storage"
	"github.com/alternayte/sayso/internal/testenv"
)

const (
	minioImage   = "cgr.dev/chainguard/minio@sha256:96b6cf3e14084d3427ec46de10c2873af61b945ad9272eec266cfc8df551cba5"
	azuriteImage = "mcr.microsoft.com/azure-storage/azurite:3.37.0"
	azuriteKey   = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
)

// conformance runs the suite of §15.2 against one store.
func conformance(t *testing.T, s storage.Store) {
	t.Helper()
	ctx := context.Background()
	prefix := "conf/" + uuid.NewString() + "/"
	data := []byte(strings.Repeat("sayso ", 1000))
	for i := 0; i < 3; i++ {
		if err := s.Put(ctx, fmt.Sprintf("%sa/%d.json.gz", prefix, i), bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	rc, err := s.Get(ctx, prefix+"a/1.json.gz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("get returned %d bytes", len(got))
	}
	info, err := s.Stat(ctx, prefix+"a/1.json.gz")
	if err != nil || info.Size != int64(len(data)) {
		t.Fatalf("stat: %+v %v", info, err)
	}
	keys, err := s.List(ctx, prefix+"a/")
	slices.Sort(keys)
	if err != nil || len(keys) != 3 || keys[0] != prefix+"a/0.json.gz" {
		t.Fatalf("list: %v %v", keys, err)
	}
	// A second put replaces the object.
	if err := s.Put(ctx, prefix+"a/1.json.gz", bytes.NewReader([]byte("v2")), 2); err != nil {
		t.Fatal(err)
	}
	if info, _ := s.Stat(ctx, prefix+"a/1.json.gz"); info.Size != 2 {
		t.Fatalf("replace: size %d", info.Size)
	}
	if err := s.Delete(ctx, prefix+"a/1.json.gz"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, prefix+"a/1.json.gz"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if _, err := s.Stat(ctx, prefix+"missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("stat of a missing key: %v", err)
	}
	if err := s.Delete(ctx, prefix+"missing"); err != nil {
		t.Fatalf("delete of a missing key: %v", err)
	}
}

func TestSCN_OBS_003_StoreConformance(t *testing.T) {
	ctx := context.Background()
	dbURL := testenv.Postgres(t)
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := app.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	mc, err := tcminio.Run(ctx, minioImage, tcminio.WithUsername("sayso-test"), tcminio.WithPassword("sayso-test-secret"))
	if err != nil {
		t.Fatalf("minio: %v", err)
	}
	t.Cleanup(func() { _ = mc.Terminate(context.Background()) })
	endpoint, err := mc.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4("sayso-test", "sayso-test-secret", "")})
	if err := admin.MakeBucket(ctx, "sayso", minio.MakeBucketOptions{}); err != nil {
		t.Fatal(err)
	}
	s3cfg := storage.S3Config{Endpoint: endpoint, Bucket: "sayso", PathStyle: true, Insecure: true, AccessKey: "sayso-test", Prefix: "runs-store"}

	// Azurite lags the API version of the Azure SDK.
	az, err := azurite.Run(ctx, azuriteImage, testcontainers.WithCmdArgs("--skipApiVersionCheck"))
	if err != nil {
		t.Fatalf("azurite: %v", err)
	}
	t.Cleanup(func() { _ = az.Terminate(context.Background()) })
	blobURL, err := az.BlobServiceURL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	accountURL := strings.TrimRight(blobURL, "/") + "/devstoreaccount1"
	cred, _ := azblob.NewSharedKeyCredential("devstoreaccount1", azuriteKey)
	ac, err := azblob.NewClientWithSharedKeyCredential(accountURL, cred, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ac.CreateContainer(ctx, "sayso", nil); err != nil {
		t.Fatal(err)
	}
	azcfg := storage.AzConfig{AccountURL: accountURL, Container: "sayso", Auth: "key"}

	stores := map[string]func() (storage.Store, error){
		"postgres": func() (storage.Store, error) { return storage.Postgres{Pool: pool}, nil },
		"s3":       func() (storage.Store, error) { return storage.NewS3(s3cfg, "sayso-test-secret") },
		"azblob":   func() (storage.Store, error) { return storage.NewAzBlob(azcfg, azuriteKey) },
	}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			s, err := open()
			if err != nil {
				t.Fatal(err)
			}
			conformance(t, s)
		})
	}

	// After the default changes from postgres to s3, old objects still open.
	reg := &storage.Registry{Pool: pool, Secret: func(context.Context, uuid.UUID) (string, error) { return "sayso-test-secret", nil }}
	oldID, old, err := reg.Default(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Put(ctx, "runs/old/artifacts/result.json", strings.NewReader(`{"old":true}`), 12); err != nil {
		t.Fatal(err)
	}
	secretID := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `INSERT INTO secrets (id, name, kind, env_name, updated_by) VALUES ($1, 'store.s3.secret', 'env_ref', 'SAYSO_TEST_S3_SECRET', 'test')`, secretID); err != nil {
		t.Fatalf("secret row: %v", err)
	}
	cfg, _ := json.Marshal(s3cfg)
	newID := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `UPDATE stores SET is_default = false`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO stores (id, name, driver, config, secret_id, is_default) VALUES ($1, 's3', 's3', $2, $3, true)`, newID, cfg, secretID); err != nil {
		t.Fatal(err)
	}
	gotID, s3, err := reg.Default(ctx)
	if err != nil || gotID != newID {
		t.Fatalf("default after the change: %v %v", gotID, err)
	}
	if err := s3.Put(ctx, "runs/new/artifacts/result.json", strings.NewReader("{}"), 2); err != nil {
		t.Fatal(err)
	}
	prev, err := reg.ByID(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := prev.Get(ctx, "runs/old/artifacts/result.json")
	if err != nil {
		t.Fatalf("old object after the default changed: %v", err)
	}
	_ = rc.Close()
}
