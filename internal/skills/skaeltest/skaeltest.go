// Package skaeltest starts the released Skael server in a testcontainer and
// seeds it with an owner, a review skill, the set pr-review-set and a service
// account with an agent key and a reporter key (§23.4).
package skaeltest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Image is the pinned Skael server image.
const Image = "ghcr.io/skael-dev/skael:0.21.4"

// Set is the seeded set, SkillName its review skill.
const (
	Set       = "pr-review-set"
	SkillName = "pr-review"
)

// SkillText is the SKILL.md of the seeded review skill.
const SkillText = "---\nname: pr-review\ndescription: Acme review skill from Skael.\n---\n\n# Acme review skill (Skael)\n\nCheck money paths first.\n"

// Skael is a running seeded Skael server.
type Skael struct {
	URL         string // from the test process
	Port        string // mapped port, for containers through host.docker.internal
	AgentKey    string // skills:read, sets:read, events:write
	ReporterKey string // events:write, outcomes:write
	ownerKey    string
	container   testcontainers.Container
	db          testcontainers.Container
}

var claimRe = regexp.MustCompile(`"claim_code":"([^"]+)"`)

// Start runs Skael on a PostgreSQL container and seeds it.
func Start(t *testing.T) *Skael {
	t.Helper()
	ctx := context.Background()
	net, err := network.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = net.Remove(context.Background()) })
	pg, err := testcontainers.Run(ctx, "postgres:16",
		network.WithNetwork([]string{"db"}, net),
		testcontainers.WithEnv(map[string]string{"POSTGRES_USER": "skael", "POSTGRES_PASSWORD": "skael", "POSTGRES_DB": "skael"}),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start skael postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	c, err := testcontainers.Run(ctx, Image,
		network.WithNetwork([]string{"skael"}, net),
		testcontainers.WithEnv(map[string]string{"DATABASE_URL": "postgres://skael:skael@db:5432/skael?sslmode=disable", "STORAGE_PATH": "/tmp/skills", "LOG_FORMAT": "json"}),
		testcontainers.WithExposedPorts("8080/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/api/health").WithPort("8080/tcp").WithStartupTimeout(120*time.Second)),
	)
	if err != nil {
		t.Fatalf("start skael: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "8080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	s := &Skael{URL: fmt.Sprintf("http://%s:%s", host, port.Port()), Port: port.Port(), container: c, db: pg}
	s.seed(t)
	return s
}

// Stop stops Skael before the test ends, for the "Skael is down" cases.
func (s *Skael) Stop(t *testing.T) {
	t.Helper()
	timeout := 5 * time.Second
	if err := s.container.Stop(context.Background(), &timeout); err != nil {
		t.Fatal(err)
	}
}

func (s *Skael) claimCode(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		rc, err := s.container.Logs(context.Background())
		if err == nil {
			b, _ := io.ReadAll(rc)
			_ = rc.Close()
			if m := claimRe.FindSubmatch(b); m != nil {
				return string(m[1])
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("skael did not log a claim code")
	return ""
}

// call sends a JSON request and decodes a JSON answer.
func call(t *testing.T, c *http.Client, method, url, key string, in, out any) {
	t.Helper()
	var body io.Reader
	if b, ok := in.([]byte); ok {
		body = bytes.NewReader(b)
	} else if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, body)
	switch in.(type) {
	case nil:
	case []byte:
		req.Header.Set("Content-Type", "application/gzip")
	default:
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if u, err := neturl.Parse(url); err == nil {
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: %d %s", method, url, resp.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, url, err, b)
		}
	}
}

func skillArchive(text string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "SKILL.md", Mode: 0o644, Size: int64(len(text)), ModTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	_, _ = tw.Write([]byte(text))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func (s *Skael) seed(t *testing.T) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	call(t, c, http.MethodPost, s.URL+"/api/setup/claim", "", map[string]string{"code": s.claimCode(t)}, nil)
	call(t, c, http.MethodPost, s.URL+"/api/auth/sign-up/email", "", map[string]string{"email": "owner@acme.test", "password": "acme-owner-password-1", "name": "Acme Owner"}, nil)
	var key struct {
		Key string `json:"key"`
	}
	call(t, c, http.MethodPost, s.URL+"/api/auth/keys", "", map[string]any{"name": "seed"}, &key)
	s.ownerKey = key.Key
	plain := &http.Client{Timeout: 30 * time.Second}
	call(t, plain, http.MethodPost, s.URL+"/api/skills", s.ownerKey, map[string]string{"name": SkillName, "description": "Acme review skill"}, nil)
	call(t, plain, http.MethodPost, s.URL+"/api/skills/"+SkillName+"/versions", s.ownerKey, skillArchive(SkillText), nil)
	var set struct {
		ID string `json:"id"`
	}
	call(t, plain, http.MethodPost, s.URL+"/api/sets", s.ownerKey, map[string]any{"name": Set, "entries": []map[string]any{{"name": SkillName}}}, &set)
	var acct struct {
		ID string `json:"id"`
	}
	call(t, plain, http.MethodPost, s.URL+"/api/service-accounts", s.ownerKey, map[string]string{"name": "sayso"}, &acct)
	call(t, plain, http.MethodPost, s.URL+"/api/service-accounts/"+acct.ID+"/keys", s.ownerKey,
		map[string]any{"name": "agent", "scopes": []string{"skills:read", "sets:read", "events:write"}, "set_id": set.ID}, &key)
	s.AgentKey = key.Key
	call(t, plain, http.MethodPost, s.URL+"/api/service-accounts/"+acct.ID+"/keys", s.ownerKey,
		map[string]any{"name": "reporter", "scopes": []string{"events:write", "outcomes:write"}}, &key)
	s.ReporterKey = key.Key
}

// Get reads a JSON document of the API with the owner key.
func (s *Skael) Get(t *testing.T, path string, out any) {
	t.Helper()
	call(t, &http.Client{Timeout: 30 * time.Second}, http.MethodGet, s.URL+path, s.ownerKey, nil, out)
}

// Query runs a SQL query in Skael's database and returns the output of psql.
func (s *Skael) Query(t *testing.T, sql string) string {
	t.Helper()
	code, out, err := s.db.Exec(context.Background(), []string{"psql", "-U", "skael", "-d", "skael", "-At", "-c", sql})
	if err != nil || code != 0 {
		t.Fatalf("skael query: %d %v", code, err)
	}
	b, _ := io.ReadAll(out)
	// Exec output has an 8-byte stream header per frame.
	clean := []byte{}
	for len(b) >= 8 {
		n := int(b[4])<<24 | int(b[5])<<16 | int(b[6])<<8 | int(b[7])
		if 8+n > len(b) {
			break
		}
		clean = append(clean, b[8:8+n]...)
		b = b[8+n:]
	}
	return strings.TrimSpace(string(clean))
}
