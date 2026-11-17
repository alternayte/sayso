package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type tokenServer struct {
	mu      sync.Mutex
	minted  int
	expires time.Time
	valid   map[string]bool
}

func (s *tokenServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodPost && r.URL.Path == "/app/installations/7/access_tokens" {
		s.minted++
		tok := fmt.Sprintf("tok-%d", s.minted)
		s.valid[tok] = true
		_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_at": s.expires})
		return
	}
	if !s.valid[r.Header.Get("Authorization")[len("Bearer "):]] {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		return
	}
	_, _ = w.Write([]byte(`{"permission":"write"}`))
}

func testApp(t *testing.T) *App {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	return &App{AppID: 1, PrivateKeyPEM: string(p)}
}

// SCN-GH-012: the cache follows expires_at by the wall clock.
func TestTokensFollowExpiresAt(t *testing.T) {
	now := time.Date(2026, 11, 17, 16, 27, 0, 0, time.UTC)
	srv := &tokenServer{expires: now.Add(time.Hour), valid: map[string]bool{}}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	c := &Client{APIURL: hs.URL, HTTP: hs.Client()}
	clock := now
	tokens := NewTokens()
	tokens.Now = func() time.Time { return clock }
	app := testApp(t)
	ctx := context.Background()

	a, err := tokens.Get(ctx, c, app, 7)
	if err != nil {
		t.Fatal(err)
	}
	clock = now.Add(49 * time.Minute)
	if b, _ := tokens.Get(ctx, c, app, 7); b != a {
		t.Fatalf("token renewed %v before expiry minus margin", clock.Sub(now))
	}
	clock = now.Add(51 * time.Minute)
	if b, _ := tokens.Get(ctx, c, app, 7); b == a {
		t.Fatal("token reused within 10 minutes of expires_at")
	}
	if srv.minted != 2 {
		t.Fatalf("minted %d tokens, want 2", srv.minted)
	}
}

// SCN-GH-013: a 401 renews the cached token and retries once.
func TestCallRenewsTokenAfter401(t *testing.T) {
	srv := &tokenServer{expires: time.Now().Add(time.Hour), valid: map[string]bool{}}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	tokens := NewTokens()
	app := testApp(t)
	c := &Client{APIURL: hs.URL, HTTP: hs.Client()}
	c.Renew = func(ctx context.Context, c *Client, inst int64, stale string) (string, error) {
		return tokens.Renew(ctx, c, app, inst, stale)
	}
	ctx := context.Background()
	tok, err := tokens.Get(ctx, c, app, 7)
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	delete(srv.valid, tok)
	srv.mu.Unlock()

	if _, err := c.Permission(ctx, tok, 7, "acme/payments", "octo"); err != nil {
		t.Fatalf("call after 401: %v", err)
	}
	if srv.minted != 2 {
		t.Fatalf("minted %d tokens, want 2", srv.minted)
	}
	if next, _ := tokens.Get(ctx, c, app, 7); next == tok {
		t.Fatal("stale token still cached")
	}
}
