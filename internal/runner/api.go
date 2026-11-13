package runner

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/storage"
)

// MaxArtifact is the largest artifact a run may upload.
const MaxArtifact = 64 << 20

// API serves the runner protocol (Appendix B) for agent runs.
type API struct {
	Pool   *pgxpool.Pool
	Stores *storage.Registry
	// Enrich adds credentials and proxy entries to the stored spec.
	Enrich func(ctx context.Context, run AgentRun, spec *Spec) error
	// Redact replaces known secret values.
	Redact func(string) string
	Now    func() time.Time
	// OnComplete runs after a run ended, for the summary events.
	OnComplete func(ctx context.Context, run uuid.UUID) error
}

type runKey struct{}

// Token patterns that are redacted in every run text, whatever process
// minted them.
var tokenPatterns = []struct {
	re   *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`srt_[A-Za-z0-9_-]{20,}`), "run_token"},
	{regexp.MustCompile(`gh[suop]_[A-Za-z0-9]{20,}`), "github_token"},
	{regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`), "github_token"},
}

// RedactRun removes run tokens, GitHub tokens and the known secrets from
// text written by a run (§15.1).
func (a *API) RedactRun(s string) string {
	for _, p := range tokenPatterns {
		s = p.re.ReplaceAllString(s, "[redacted:"+p.name+"]")
	}
	if a.Redact != nil {
		s = a.Redact(s)
	}
	return s
}

func (a *API) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Handler returns the runner routes for mounting at /api/runner/v1.
func (a *API) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(a.auth)
	// Artifact names hold slashes (tests/<path>), so this route is outside Huma.
	r.Put("/artifacts/*", a.putArtifact)
	api := NewAPI(r)
	a.register(api)
	return r
}

func (a *API) putArtifact(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(chi.URLParam(r, "*"))
	if !artifactName.MatchString(name) || strings.Contains(name, "..") {
		http.Error(w, "artifact name: use session.jsonl.gz, result.json, manifest.json or tests/<path>", http.StatusUnprocessableEntity)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxArtifact))
	if err != nil {
		http.Error(w, "artifact is larger than 64 MB", http.StatusRequestEntityTooLarge)
		return
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	if err := a.PutArtifact(r.Context(), runFrom(r.Context()).ID, name, ct, data); err != nil {
		http.Error(w, "artifact not stored", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// NewAPI creates the Huma API of the runner protocol. `sayso openapi
// --runner` exports its document.
func NewAPI(r chi.Router) huma.API {
	huma.DefaultArrayNullable = false
	cfg := huma.DefaultConfig("Sayso runner API", "1.0.0")
	cfg.Servers = []*huma.Server{{URL: "/api/runner/v1"}}
	cfg.OpenAPIPath = "/openapi"
	cfg.DocsPath = ""
	cfg.SchemasPath = ""
	cfg.CreateHooks = nil
	return humachi.New(r, cfg)
}

// OpenAPI returns the runner API document.
func OpenAPI() ([]byte, error) {
	api := NewAPI(chi.NewRouter())
	(&API{}).register(api)
	return json.MarshalIndent(api.OpenAPI(), "", "  ")
}

func (a *API) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi.json" || r.URL.Path == "/openapi.yaml" {
			next.ServeHTTP(w, r)
			return
		}
		h := r.Header.Get("Authorization")
		run, err := Active(r.Context(), a.Pool, strings.TrimPrefix(h, "Bearer "), a.now())
		if err != nil || !strings.HasPrefix(h, "Bearer ") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"title":"Unauthorized","status":401,"detail":"the run token is not valid"}`)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), runKey{}, run)))
	})
}

func runFrom(ctx context.Context) AgentRun {
	r, _ := ctx.Value(runKey{}).(AgentRun)
	return r
}

type EventsBody struct {
	Events []Event `json:"events" maxItems:"500"`
}

type HeartbeatBody struct {
	Cancel bool `json:"cancel"`
}

var artifactName = regexp.MustCompile(`^(session\.jsonl\.gz|result\.json|manifest\.json|tests/[A-Za-z0-9._/-]{1,200})$`)

func (a *API) register(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "get-spec", Method: http.MethodGet, Path: "/spec", Tags: []string{"runner"}},
		func(ctx context.Context, _ *struct{}) (*struct{ Body Spec }, error) {
			run := runFrom(ctx)
			var spec Spec
			if err := json.Unmarshal(run.Spec, &spec); err != nil {
				return nil, fmt.Errorf("stored spec: %w", err)
			}
			spec.RunID = run.ID.String()
			spec.Kind = run.Kind
			if a.Enrich != nil {
				if err := a.Enrich(ctx, run, &spec); err != nil {
					return nil, huma.Error503ServiceUnavailable(a.RedactRun(err.Error()))
				}
			}
			spec.normalize()
			return &struct{ Body Spec }{spec}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "post-events", Method: http.MethodPost, Path: "/events", Tags: []string{"runner"}, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *struct{ Body EventsBody }) (*struct{}, error) {
			return nil, a.storeEvents(ctx, runFrom(ctx), in.Body.Events)
		})

	huma.Register(api, huma.Operation{OperationID: "post-heartbeat", Method: http.MethodPost, Path: "/heartbeat", Tags: []string{"runner"}},
		func(ctx context.Context, _ *struct{}) (*struct{ Body HeartbeatBody }, error) {
			run := runFrom(ctx)
			cancel, err := New(a.Pool).Heartbeat(ctx, run.ID)
			if err != nil {
				return nil, err
			}
			return &struct{ Body HeartbeatBody }{HeartbeatBody{Cancel: cancel}}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "post-complete", Method: http.MethodPost, Path: "/complete", Tags: []string{"runner"}, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *struct{ Body Complete }) (*struct{}, error) {
			run := runFrom(ctx)
			c := in.Body
			if c.ExitStatus == "completed" && c.Result == nil {
				return nil, huma.Error422UnprocessableEntity("result: a completed run must send its result")
			}
			if c.Result != nil {
				if c.Result.RunID != run.ID.String() || c.Result.Kind != run.Kind {
					return nil, huma.Error422UnprocessableEntity("result: run_id and kind must match the run")
				}
				raw, err := json.MarshalIndent(c.Result, "", "  ")
				if err != nil {
					return nil, err
				}
				if err := a.PutArtifact(ctx, run.ID, "result.json", "application/json", raw); err != nil {
					return nil, err
				}
				if h := c.Result.Harness; h.Name != "" {
					if err := New(a.Pool).SetHarness(ctx, SetHarnessParams{ID: run.ID, HarnessVersion: strings.TrimSpace(h.Name + " " + h.Version)}); err != nil {
						return nil, err
					}
				}
			}
			status := "done"
			switch c.ExitStatus {
			case "completed":
			case "cancelled":
				status = "cancelled"
			default:
				status = "failed"
			}
			reason := c.ExitStatus
			if c.ExitStatus == "completed" {
				reason = "completed"
			}
			_, err := New(a.Pool).FinishRun(ctx, FinishRunParams{ID: run.ID, Status: status, ExitReason: reason})
			if err == nil && a.OnComplete != nil {
				err = a.OnComplete(ctx, run.ID)
			}
			if err == nil && c.Error != "" {
				err = New(a.Pool).InsertServerEvent(ctx, InsertServerEventParams{RunID: run.ID, Type: "error", Name: "complete", Status: c.ExitStatus,
					Preview: clip(a.RedactRun(c.Error), 500)})
			}
			return nil, err
		})
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	for n > 0 && n < len(s) && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

// storeEvents indexes a batch and stores each payload, redacted, in the
// default store under runs/<run>/events/<seq>.json.gz (§15.1).
func (a *API) storeEvents(ctx context.Context, run AgentRun, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	storeID, store, err := a.Stores.Default(ctx)
	if err != nil {
		return err
	}
	rows := make([][]any, 0, len(events))
	var objs []storage.Object
	for _, e := range events {
		at, err := time.Parse(time.RFC3339Nano, e.At)
		if err != nil {
			return huma.Error422UnprocessableEntity(fmt.Sprintf("event %d: at is not RFC 3339", e.Seq))
		}
		var key *string
		if e.Payload != "" {
			raw, err := base64.StdEncoding.DecodeString(e.Payload)
			if err != nil {
				return huma.Error422UnprocessableEntity(fmt.Sprintf("event %d: payload is not base64", e.Seq))
			}
			plain, err := gunzip(raw)
			if err != nil {
				return huma.Error422UnprocessableEntity(fmt.Sprintf("event %d: payload is not gzip", e.Seq))
			}
			// Compress again only when redaction changed the text.
			gz := raw
			if red := a.RedactRun(string(plain)); red != string(plain) {
				if gz, err = gzipBytes([]byte(red)); err != nil {
					return err
				}
			}
			k := fmt.Sprintf("runs/%s/events/%d.json.gz", run.ID, e.Seq)
			objs = append(objs, storage.Object{Key: k, Data: gz})
			key = &k
		}
		var sid *uuid.UUID
		if key != nil {
			sid = &storeID
		}
		rows = append(rows, []any{run.ID, e.Seq, at, e.Stage, e.Type, e.Name, e.Status, int32(e.DurationMS), e.TokensIn, e.TokensOut,
			clip(a.RedactRun(e.Preview), 500), key, sid})
	}
	// Payloads go to the store before the index rows that point at them.
	// The postgres store writes them in the same transaction.
	_, inDB := store.(storage.Postgres)
	if !inDB {
		if err := storage.PutAll(ctx, store, nil, objs); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		if inDB {
			if err := storage.PutAll(ctx, store, tx, objs); err != nil {
				return err
			}
		}
		// A retried batch has the same sequence numbers; keep the first copy.
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE incoming (LIKE run_events) ON COMMIT DROP`); err != nil {
			return err
		}
		cols := []string{"run_id", "seq", "at", "stage", "type", "name", "status", "duration_ms", "tokens_in", "tokens_out", "preview", "payload_key", "store_id"}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"incoming"}, cols, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO run_events SELECT * FROM incoming ON CONFLICT (run_id, seq) DO NOTHING`)
		return err
	})
}

// PutArtifact stores an artifact in the default store, redacted when it is
// text or gzip text.
func (a *API) PutArtifact(ctx context.Context, runID uuid.UUID, name, contentType string, data []byte) error {
	if strings.HasSuffix(name, ".gz") {
		if plain, err := gunzip(data); err == nil {
			if data, err = gzipBytes([]byte(a.RedactRun(string(plain)))); err != nil {
				return err
			}
		}
	} else if isText(data) {
		data = []byte(a.RedactRun(string(data)))
	}
	storeID, store, err := a.Stores.Default(ctx)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("runs/%s/artifacts/%s", runID, name)
	if err := store.Put(ctx, key, bytes.NewReader(data), int64(len(data))); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	return New(a.Pool).PutArtifact(ctx, PutArtifactParams{RunID: runID, Name: name, StoreID: storeID, Key: key, Size: int64(len(data)),
		Sha256: hex.EncodeToString(sum[:]), ContentType: contentType})
}

func isText(b []byte) bool {
	n := min(len(b), 8000)
	return !bytes.ContainsRune(b[:n], 0)
}

func gunzip(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(io.LimitReader(r, 4*MaxArtifact))
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ErrRunNotFound is returned for an unknown run.
var ErrRunNotFound = errors.New("agent run not found")
