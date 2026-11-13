package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/secrets"
)

// StoreInput is a store that an admin saves (§15.2).
type StoreInput struct {
	Driver        string          `json:"driver" enum:"postgres,s3,azblob"`
	Config        json.RawMessage `json:"config,omitempty" doc:"S3Config or AzConfig"`
	Secret        *secrets.Input  `json:"secret,omitempty" doc:"s3: the secret access key; azblob: the account key or connection string"`
	RetentionDays int             `json:"retention_days" minimum:"1" maximum:"3650"`
	Default       bool            `json:"default,omitempty"`
}

type StoreBody struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Driver        string          `json:"driver"`
	Config        json.RawMessage `json:"config"`
	Secret        secrets.Field   `json:"secret"`
	RetentionDays int             `json:"retention_days"`
	Default       bool            `json:"default"`
	Objects       int64           `json:"objects" doc:"Artifacts and event payloads in this store"`
}

type TestResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	MS     int64  `json:"ms"`
}

var storeName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// API is the stores settings page (/settings/storage).
type API struct {
	Registry *Registry
	Secrets  *secrets.Store
}

func (a *API) body(ctx context.Context, row StoreRow) (StoreBody, error) {
	b := StoreBody{ID: row.ID.String(), Name: row.Name, Driver: row.Driver, Config: row.Config, RetentionDays: int(row.RetentionDays), Default: row.IsDefault}
	if len(b.Config) == 0 {
		b.Config = json.RawMessage(`{}`)
	}
	var err error
	if b.Secret, err = a.Secrets.Describe(ctx, row.SecretID); err != nil {
		return b, err
	}
	err = a.Registry.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM artifacts WHERE store_id = $1) + (SELECT count(*) FROM run_events WHERE store_id = $1 AND payload_key IS NOT NULL)`,
		row.ID).Scan(&b.Objects)
	return b, err
}

// Probe writes, reads and deletes a test object (the test button, §15.2).
func Probe(ctx context.Context, s Store) TestResult {
	start := time.Now()
	key := "sayso-test/" + uuid.NewString()
	data := []byte("sayso store test")
	fail := func(step string, err error) TestResult {
		return TestResult{Detail: step + ": " + err.Error(), MS: time.Since(start).Milliseconds()}
	}
	if err := s.Put(ctx, key, bytes.NewReader(data), int64(len(data))); err != nil {
		return fail("write", err)
	}
	rc, err := s.Get(ctx, key)
	if err != nil {
		return fail("read", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Equal(got, data) {
		return fail("read", fmt.Errorf("the object came back with %d bytes", len(got)))
	}
	if err := s.Delete(ctx, key); err != nil {
		return fail("delete", err)
	}
	return TestResult{OK: true, Detail: "write, read and delete passed", MS: time.Since(start).Milliseconds()}
}

// Register adds the store operations for admins.
func (a *API) Register(api huma.API, admin map[string]any, actor func(context.Context) audit.Actor) {
	tags := []string{"storage"}
	huma.Register(api, huma.Operation{OperationID: "list-stores", Method: http.MethodGet, Path: "/stores", Tags: tags, Metadata: admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []StoreBody }, error) {
			rows, err := New(a.Registry.Pool).ListStores(ctx)
			if err != nil {
				return nil, err
			}
			out := []StoreBody{}
			for _, r := range rows {
				b, err := a.body(ctx, r)
				if err != nil {
					return nil, err
				}
				out = append(out, b)
			}
			return &struct{ Body []StoreBody }{out}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "put-store", Method: http.MethodPut, Path: "/stores/{name}", Tags: tags, Metadata: admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
			Body StoreInput
		}) (*struct{ Body StoreBody }, error) {
			if !storeName.MatchString(in.Name) {
				return nil, huma.Error422UnprocessableEntity("name: use lower-case letters, digits and hyphens")
			}
			cfg := in.Body.Config
			if len(cfg) == 0 || string(cfg) == "null" {
				cfg = json.RawMessage(`{}`)
			}
			who := actor(ctx)
			cur, err := New(a.Registry.Pool).StoreByName(ctx, in.Name)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			exists := err == nil
			if exists && cur.Driver != in.Body.Driver {
				return nil, huma.Error422UnprocessableEntity("driver: a store keeps its driver; add a new store instead")
			}
			var prev *uuid.UUID
			if exists {
				prev = cur.SecretID
			}
			sid, err := a.Secrets.Apply(ctx, who, "store."+in.Name+".secret", in.Body.Secret, prev)
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			if sid == nil {
				sid = prev
			}
			secret := ""
			if sid != nil {
				if secret, err = a.Secrets.ValueOf(ctx, sid); err != nil {
					return nil, err
				}
			}
			if _, err := Open(a.Registry.Pool, in.Body.Driver, cfg, secret); err != nil {
				return nil, huma.Error422UnprocessableEntity("config: " + err.Error())
			}
			id := uuid.Must(uuid.NewV7())
			if exists {
				id = cur.ID
			}
			var row StoreRow
			err = pgx.BeginFunc(ctx, a.Registry.Pool, func(tx pgx.Tx) error {
				if in.Body.Default {
					if _, err := tx.Exec(ctx, `UPDATE stores SET is_default = false WHERE is_default AND id <> $1`, id); err != nil {
						return err
					}
				}
				if err := tx.QueryRow(ctx, `INSERT INTO stores (id, name, driver, config, secret_id, is_default, retention_days) VALUES ($1, $2, $3, $4, $5, $6, $7)
					ON CONFLICT (name) DO UPDATE SET config = EXCLUDED.config, secret_id = EXCLUDED.secret_id,
						is_default = EXCLUDED.is_default OR stores.is_default, retention_days = EXCLUDED.retention_days
					RETURNING id, name, driver, config, secret_id, is_default, retention_days, created_at`,
					id, in.Name, in.Body.Driver, cfg, sid, in.Body.Default, in.Body.RetentionDays).
					Scan(&row.ID, &row.Name, &row.Driver, &row.Config, &row.SecretID, &row.IsDefault, &row.RetentionDays, &row.CreatedAt); err != nil {
					return err
				}
				return audit.Write(ctx, tx, who, "store.put", in.Name, map[string]any{"driver": in.Body.Driver, "default": row.IsDefault, "retention_days": in.Body.RetentionDays})
			})
			if err != nil {
				return nil, err
			}
			b, err := a.body(ctx, row)
			return &struct{ Body StoreBody }{b}, err
		})

	huma.Register(api, huma.Operation{OperationID: "delete-store", Method: http.MethodDelete, Path: "/stores/{name}", Tags: tags, Metadata: admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{}, error) {
			row, err := New(a.Registry.Pool).StoreByName(ctx, in.Name)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("store not found")
			}
			if err != nil {
				return nil, err
			}
			b, err := a.body(ctx, row)
			if err != nil {
				return nil, err
			}
			if row.IsDefault || b.Objects > 0 {
				return nil, huma.Error409Conflict("the store is the default or still holds objects; old objects stay readable only while their store exists")
			}
			return nil, pgx.BeginFunc(ctx, a.Registry.Pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `DELETE FROM stores WHERE id = $1`, row.ID); err != nil {
					return err
				}
				return audit.Write(ctx, tx, actor(ctx), "store.delete", in.Name, nil)
			})
		})

	huma.Register(api, huma.Operation{OperationID: "test-store", Method: http.MethodPost, Path: "/stores/{name}/test", Tags: tags, Metadata: admin},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{ Body TestResult }, error) {
			row, err := New(a.Registry.Pool).StoreByName(ctx, in.Name)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("store not found")
			}
			if err != nil {
				return nil, err
			}
			s, err := a.Registry.ByID(ctx, row.ID)
			if err != nil {
				return &struct{ Body TestResult }{TestResult{Detail: "open: " + err.Error()}}, nil
			}
			return &struct{ Body TestResult }{Probe(ctx, s)}, nil
		})
}
