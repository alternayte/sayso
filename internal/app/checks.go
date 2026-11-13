package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/alternayte/sayso/internal/auth"
	"github.com/alternayte/sayso/internal/doctor"
	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/settings"
	"github.com/alternayte/sayso/internal/skills"
	"github.com/alternayte/sayso/internal/storage"
)

// Checks returns the diagnostics of the features that exist in this build,
// with one check per provider. Details and errors are redacted.
func Checks(ctx context.Context, d *Deps, f *Features) []doctor.Check {
	cs := staticChecks(d, f)
	if names, err := f.Models.Names(ctx); err != nil {
		cs = append(cs, doctor.Check{Name: "providers", Run: func(context.Context) (string, error) { return "", err }})
	} else {
		for _, n := range names {
			cs = append(cs, doctor.Check{Name: "provider " + n, Run: func(ctx context.Context) (string, error) { return f.Models.Check(ctx, n) }})
		}
	}
	if names, err := f.MCP.Names(ctx); err != nil {
		cs = append(cs, doctor.Check{Name: "mcp_servers", Run: func(context.Context) (string, error) { return "", err }})
	} else {
		for _, n := range names {
			cs = append(cs, doctor.Check{Name: "mcp " + n, Run: func(ctx context.Context) (string, error) { return f.MCP.Check(ctx, n) }})
		}
	}
	if stores, err := storage.New(d.Pool).ListStores(ctx); err != nil {
		cs = append(cs, doctor.Check{Name: "stores", Run: func(context.Context) (string, error) { return "", err }})
	} else {
		for _, st := range stores {
			cs = append(cs, doctor.Check{Name: "store " + st.Name, Run: func(ctx context.Context) (string, error) {
				s, err := f.Stores.ByID(ctx, st.ID)
				if err != nil {
					return "", err
				}
				r := storage.Probe(ctx, s)
				if !r.OK {
					return "", errors.New(r.Detail)
				}
				return fmt.Sprintf("%s: write, read and delete in %d ms", st.Driver, r.MS), nil
			}})
		}
	}
	cs = append(cs, doctor.Check{Name: "skael", Run: func(ctx context.Context) (string, error) { return skaelCheck(ctx, d, f) }})
	for i := range cs {
		run := cs[i].Run
		cs[i].Run = func(ctx context.Context) (string, error) {
			detail, err := run(ctx)
			if err != nil {
				return "", errors.New(d.Redactor.Redact(err.Error()))
			}
			return d.Redactor.Redact(detail), nil
		}
	}
	return cs
}

func staticChecks(d *Deps, f *Features) []doctor.Check {
	return []doctor.Check{
		{Name: "database", Run: func(ctx context.Context) (string, error) {
			var v int
			if err := d.Pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&v); err != nil {
				return "", err
			}
			if v < 160000 {
				return "", fmt.Errorf("PostgreSQL %d.%d is too old: use 16 or later", v/10000, v%10000)
			}
			cur, err := MigrationsCurrent(ctx, d.Pool)
			if err != nil {
				return "", err
			}
			if !cur {
				return "", errors.New("migrations are pending")
			}
			return fmt.Sprintf("PostgreSQL %d.%d, migrations current", v/10000, v%10000), nil
		}},
		{Name: "secret_key", Run: func(ctx context.Context) (string, error) {
			n, err := f.Secrets.CheckAll(ctx)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%d stored secrets open with the key", n), nil
		}},
		{Name: "temporal", Run: func(ctx context.Context) (string, error) {
			if _, err := d.Temporal.CheckHealth(ctx, nil); err != nil {
				return "", fmt.Errorf("%s: %w", d.Config.TemporalAddress, err)
			}
			ns, err := d.Temporal.WorkflowService().DescribeNamespace(ctx, &workflowservice.DescribeNamespaceRequest{Namespace: d.Config.TemporalNamespace})
			if err != nil {
				return "", fmt.Errorf("namespace %s: %w", d.Config.TemporalNamespace, err)
			}
			return fmt.Sprintf("%s, namespace %s", d.Config.TemporalAddress, ns.NamespaceInfo.GetName()), nil
		}},
		{Name: "github_app", Run: func(ctx context.Context) (string, error) {
			a, err := github.LoadApp(ctx, d.Pool, f.Secrets)
			if err != nil {
				return "", err
			}
			gh, err := f.GitHubClient(ctx)
			if err != nil {
				return "", err
			}
			if _, err := gh.OrgInstallationToken(ctx, a); err != nil {
				return "", err
			}
			return fmt.Sprintf("App %s for %s mints installation tokens", a.Slug, a.Org), nil
		}},
		{Name: "webhook_ingress", Run: func(ctx context.Context) (string, error) {
			last, err := github.New(d.Pool).LastDelivery(ctx)
			when := "no delivery received yet"
			if err == nil {
				when = "last delivery " + last.ReceivedAt.UTC().Format(time.RFC3339) + " (" + last.Event + ")"
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return "", err
			}
			if d.Config.Ingress != "relay" {
				return "direct, " + when, nil
			}
			if f.Relay == nil {
				return "", errors.New("relay ingress without a relay client")
			}
			st := f.Relay.State()
			if !st.Running {
				// This process does not hold the stream: check that the relay answers.
				return relayReachable(ctx, d, when)
			}
			if !st.Connected {
				return "", fmt.Errorf("relay stream to %s is not connected: %s", d.Config.RelayURL, st.LastError)
			}
			return "relay stream connected, " + when, nil
		}},
		{Name: "runner", Run: func(ctx context.Context) (string, error) { return runnerCheck(ctx, d.Config) }},
		{Name: "outbound_proxy", Run: func(context.Context) (string, error) {
			if _, err := HTTPClient(d.Config); err != nil {
				return "", err
			}
			var parts []string
			for _, v := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"} {
				if os.Getenv(v) != "" || os.Getenv(strings.ToLower(v)) != "" {
					parts = append(parts, v+" set")
				}
			}
			if d.Config.ExtraCAPEM != "" || d.Config.ExtraCAFile != "" {
				parts = append(parts, "extra CA loaded")
			}
			if len(parts) == 0 {
				return "direct connections, system CAs", nil
			}
			return strings.Join(parts, ", "), nil
		}},
	}
}

// RegisterNamespace creates the Temporal namespace with 7 days retention
// when it is missing and the setting allows it (§18.3).
func RegisterNamespace(ctx context.Context, d *Deps, st *settings.Store) error {
	allowed := true
	if row, err := st.Get(ctx, settings.RegisterTemporalNS); err == nil {
		allowed = string(row.Value) == "true"
	}
	if !allowed {
		return nil
	}
	ws := d.Temporal.WorkflowService()
	_, err := ws.DescribeNamespace(ctx, &workflowservice.DescribeNamespaceRequest{Namespace: d.Config.TemporalNamespace})
	var nf *serviceerror.NamespaceNotFound
	if !errors.As(err, &nf) {
		return err
	}
	_, err = ws.RegisterNamespace(ctx, &workflowservice.RegisterNamespaceRequest{
		Namespace:                        d.Config.TemporalNamespace,
		WorkflowExecutionRetentionPeriod: durationpb.New(7 * 24 * time.Hour),
	})
	var exists *serviceerror.NamespaceAlreadyExists
	if errors.As(err, &exists) {
		return nil
	}
	return err
}

type DiagnosticsBody struct {
	OK     bool            `json:"ok"`
	Checks []doctor.Result `json:"checks"`
}

func registerDiagnostics(api huma.API, d *Deps, f *Features) {
	huma.Register(api, huma.Operation{
		OperationID: "get-diagnostics", Method: http.MethodGet, Path: "/diagnostics", Tags: []string{"diagnostics"}, Metadata: auth.Op(auth.Admin),
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body DiagnosticsBody }, error) {
		rs := doctor.Run(ctx, Checks(ctx, d, f))
		return &struct{ Body DiagnosticsBody }{DiagnosticsBody{OK: doctor.AllOK(rs), Checks: rs}}, nil
	})
}

// AgentRuntime is the agent runtime entry point in the agent image.
const AgentRuntime = "/opt/sayso/agent/dist/main.js"

// runnerCheck checks that the configured runner can start agent runs.
func runnerCheck(_ context.Context, cfg *Config) (string, error) {
	if cfg.Runner == "kubernetes" {
		return kubernetesCheck(cfg)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return "", errors.New("process runner: node is not on PATH; run the worker in the agent image")
	}
	if _, err := os.Stat(AgentRuntime); err != nil {
		return "", fmt.Errorf("process runner: agent runtime %s: %w", AgentRuntime, err)
	}
	return "process runner, " + node, nil
}

func kubernetesCheck(cfg *Config) (string, error) {
	k, err := KubernetesRunner(cfg)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := k.CheckPermissions(ctx); err != nil {
		return "", err
	}
	return "kubernetes runner, namespace " + k.Namespace + ", image " + k.Image, nil
}

func relayReachable(ctx context.Context, d *Deps, when string) (string, error) {
	hc, err := HTTPClient(d.Config)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(d.Config.RelayURL, "/")+"/healthz", nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("relay %s: %w", d.Config.RelayURL, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("relay %s: health returned %d", d.Config.RelayURL, resp.StatusCode)
	}
	return "relay answers; the API process holds the stream (see Diagnostics), " + when, nil
}

// skaelCheck reports whether Skael is off, or reachable with an agent key.
func skaelCheck(ctx context.Context, d *Deps, f *Features) (string, error) {
	url, err := f.Settings.String(ctx, settings.SkaelURL)
	if err != nil {
		return "", err
	}
	if url == "" {
		return "Skael is off", nil
	}
	set, _ := f.Settings.String(ctx, settings.SkaelDefaultSet)
	if _, err := f.Secrets.Value(ctx, skills.AgentKeySecret); err != nil {
		return "", fmt.Errorf("the Skael agent key: %w", err)
	}
	hc, err := HTTPClient(d.Config)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(url, "/")+"/api/health", nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("the Skael server %s: %w", url, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the Skael server %s: health returned %d", url, resp.StatusCode)
	}
	return fmt.Sprintf("Skael %s answers, default set %q, agent key stored", url, set), nil
}
