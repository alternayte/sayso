//go:build contract

package github_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/github"
)

func TestSCN_GH_005_RelayBuffersAndStreams(t *testing.T) {
	const token = "relay-token-for-tests"
	e := apptest.New(t, apptest.Options{Env: map[string]string{"SAYSO_INGRESS": "relay", "SAYSO_RELAY_TOKEN": token}})
	relay := github.NewRelay(token, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rs := httptest.NewServer(relay.Handler())
	defer rs.Close()
	e.GitHub.SetHook(rs.URL+"/webhooks/github", apptest.WebhookSecret)

	client := e.F.Relay
	if client == nil {
		t.Fatal("relay ingress did not create the relay client")
	}
	client.URL = rs.URL
	connect := func() context.CancelFunc {
		ctx, cancel := context.WithCancel(context.Background())
		go client.Run(ctx)
		deadline := time.Now().Add(10 * time.Second)
		for !client.State().Connected {
			if time.Now().After(deadline) {
				t.Fatalf("relay stream did not connect: %+v", client.State())
			}
			time.Sleep(20 * time.Millisecond)
		}
		return cancel
	}

	// The first connection gives the relay the webhook secret, then the API goes away.
	stop := connect()
	stop()
	for client.State().Connected {
		time.Sleep(20 * time.Millisecond)
	}

	var sent []string
	for _, name := range []string{"acme/payments", "acme/web", "acme/shared-go"} {
		guid, code := e.GitHub.Install(name)
		if code != http.StatusAccepted {
			t.Fatalf("relay answered %d", code)
		}
		sent = append(sent, guid)
	}
	for _, action := range []string{"removed", "added"} {
		guid, code := e.GitHub.ChangeRepositories(action, "acme/web")
		if code != http.StatusAccepted {
			t.Fatalf("relay answered %d", code)
		}
		sent = append(sent, guid)
	}
	if relay.Pending() != 5 {
		t.Fatalf("relay holds %d events", relay.Pending())
	}
	if n := e.Count(t, `SELECT count(*) FROM webhook_deliveries`); n != 0 {
		t.Fatalf("the API received %d deliveries while the stream was down", n)
	}

	stop = connect()
	defer stop()
	deadline := time.Now().Add(15 * time.Second)
	for relay.Pending() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("relay still holds %d events", relay.Pending())
		}
		time.Sleep(50 * time.Millisecond)
	}
	rows, err := e.Deps.Pool.Query(context.Background(), `SELECT delivery_id FROM webhook_deliveries WHERE received_via = 'relay' ORDER BY received_at`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		got = append(got, d)
	}
	rows.Close()
	if len(got) != 5 {
		t.Fatalf("API stored %d relay deliveries", len(got))
	}
	// In order: the last event re-added acme/web.
	if n := e.Count(t, `SELECT count(*) FROM repositories WHERE removed_at IS NULL`); n != 3 {
		t.Fatalf("repositories after the ordered events: %d", n)
	}
	if !slices.Equal(got, sent) {
		t.Fatalf("deliveries arrived out of order:\n got %v\nsent %v", got, sent)
	}

	// The stream needs the relay token.
	resp, err := http.Get(rs.URL + "/relay/v1/stream")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stream without token: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, rs.URL+"/relay/v1/stream", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stream with a wrong token: %d", resp.StatusCode)
	}

	// The relay container has no writable volume.
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../deploy/compose/compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			ReadOnly    bool     `yaml:"read_only"`
			Volumes     []any    `yaml:"volumes"`
			Tmpfs       []string `yaml:"tmpfs"`
			Command     []string `yaml:"command"`
			Environment map[string]string
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatal(err)
	}
	r, ok := compose.Services["relay"]
	if !ok {
		t.Fatal("compose.yaml has no relay service")
	}
	if !r.ReadOnly || len(r.Volumes) != 0 || len(r.Tmpfs) != 0 {
		t.Fatalf("relay service: read_only %v, volumes %v, tmpfs %v", r.ReadOnly, r.Volumes, r.Tmpfs)
	}
	if _, has := r.Environment["SAYSO_DATABASE_URL"]; has {
		t.Fatal("the relay service has database access")
	}
}
