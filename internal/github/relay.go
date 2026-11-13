package github

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Relay limits (§6.2).
const (
	RelayMaxEvents = 10000
	RelayMaxAge    = 24 * time.Hour
	// SecretHeader carries the webhook secret from the API to the relay on
	// the stream request. The relay keeps it in memory only.
	SecretHeader = "X-Sayso-Webhook-Secret"
)

// RelayEvent is one verified delivery in the relay buffer.
type RelayEvent struct {
	Event     string    `json:"event"`
	Delivery  string    `json:"delivery"`
	Signature string    `json:"signature"`
	Body      []byte    `json:"body"`
	At        time.Time `json:"at"`
}

// Relay is the relay role: it checks signatures, buffers events in memory
// and streams them to the API (REQ-GH-005, SI-15). It has no database and
// writes nothing to disk.
type Relay struct {
	Token string
	Now   func() time.Time
	Log   *slog.Logger

	mu     sync.Mutex
	secret string
	events []RelayEvent
	wake   chan struct{}
}

// NewRelay returns a relay that streams to clients with token.
func NewRelay(token string, log *slog.Logger) *Relay {
	return &Relay{Token: token, Now: time.Now, Log: log, wake: make(chan struct{})}
}

// Handler serves the webhook, stream, ack and health routes.
func (r *Relay) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/github", r.receive)
	mux.HandleFunc("GET /relay/v1/stream", r.stream)
	mux.HandleFunc("POST /relay/v1/ack", r.ack)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func (r *Relay) authorized(req *http.Request) bool {
	got := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	return r.Token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(r.Token)) == 1
}

func (r *Relay) receive(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, MaxWebhookBody))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	h := HeadersFrom(req.Header)
	r.mu.Lock()
	secret := r.secret
	r.mu.Unlock()
	if secret == "" {
		// The API has not connected yet, so the relay cannot check the
		// signature. GitHub records a failed delivery and the reconciler
		// asks for it again.
		http.Error(w, "relay not connected to the API", http.StatusServiceUnavailable)
		return
	}
	if !ValidSignature(secret, body, h.Signature) {
		r.Log.Warn("relay refused a delivery with a wrong signature", "delivery", h.Delivery)
		http.Error(w, "signature check failed", http.StatusUnauthorized)
		return
	}
	if h.Delivery == "" || h.Event == "" {
		http.Error(w, "delivery headers missing", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.prune()
	if len(r.events) >= RelayMaxEvents {
		r.events = r.events[1:]
	}
	r.events = append(r.events, RelayEvent{Event: h.Event, Delivery: h.Delivery, Signature: h.Signature, Body: body, At: r.Now()})
	close(r.wake)
	r.wake = make(chan struct{})
	r.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

// prune drops events older than RelayMaxAge. Call with mu held.
func (r *Relay) prune() {
	cut := r.Now().Add(-RelayMaxAge)
	i := 0
	for i < len(r.events) && r.events[i].At.Before(cut) {
		i++
	}
	r.events = r.events[i:]
}

// Pending returns the number of buffered events.
func (r *Relay) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune()
	return len(r.events)
}

func (r *Relay) stream(w http.ResponseWriter, req *http.Request) {
	if !r.authorized(req) {
		http.Error(w, "relay token required", http.StatusUnauthorized)
		return
	}
	if s := req.Header.Get(SecretHeader); s != "" {
		r.mu.Lock()
		r.secret = s
		r.mu.Unlock()
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	sent := map[string]bool{}
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		r.mu.Lock()
		r.prune()
		var batch []RelayEvent
		for _, e := range r.events {
			if !sent[e.Delivery] {
				batch = append(batch, e)
			}
		}
		wake := r.wake
		r.mu.Unlock()
		for _, e := range batch {
			b, err := json.Marshal(e)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %s\nevent: delivery\ndata: %s\n\n", e.Delivery, b); err != nil {
				return
			}
			sent[e.Delivery] = true
		}
		fl.Flush()
		select {
		case <-req.Context().Done():
			return
		case <-wake:
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
		}
	}
}

func (r *Relay) ack(w http.ResponseWriter, req *http.Request) {
	if !r.authorized(req) {
		http.Error(w, "relay token required", http.StatusUnauthorized)
		return
	}
	var in struct {
		Delivery string `json:"delivery"`
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, 4096)).Decode(&in); err != nil || in.Delivery == "" {
		http.Error(w, "delivery required", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	for i, e := range r.events {
		if e.Delivery == in.Delivery {
			r.events = append(r.events[:i:i], r.events[i+1:]...)
			break
		}
	}
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// RelayClient is the API side of relay ingress: it opens the stream
// outbound, hands each event to the ingest and acknowledges it.
type RelayClient struct {
	URL    string
	Token  string
	HTTP   *http.Client
	Ingest *Ingest
	Log    *slog.Logger

	mu        sync.Mutex
	running   bool
	connected bool
	lastErr   string
	lastEvent time.Time
}

// RelayState is the stream state for diagnostics.
type RelayState struct {
	// Running is false in a process that does not hold the stream, for
	// example the doctor command.
	Running   bool
	Connected bool
	LastError string
	LastEvent time.Time
}

// State returns the current stream state.
func (c *RelayClient) State() RelayState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return RelayState{Running: c.running, Connected: c.connected, LastError: c.lastErr, LastEvent: c.lastEvent}
}

func (c *RelayClient) set(connected bool, err error) {
	c.mu.Lock()
	c.connected = connected
	if err != nil {
		c.lastErr = err.Error()
	}
	c.mu.Unlock()
}

// Run keeps the stream open until ctx ends, and reconnects with backoff.
func (c *RelayClient) Run(ctx context.Context) {
	c.mu.Lock()
	c.running = true
	c.mu.Unlock()
	wait := time.Second
	for ctx.Err() == nil {
		err := c.once(ctx)
		c.set(false, err)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.Log.Warn("relay stream closed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < 30*time.Second {
			wait *= 2
		}
	}
}

func (c *RelayClient) once(ctx context.Context) error {
	secret, err := c.Ingest.WebhookSecret(ctx)
	if err != nil {
		return fmt.Errorf("webhook secret: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/relay/v1/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set(SecretHeader, secret)
	req.Header.Set("Accept", "text/event-stream")
	streamClient := *c.HTTP
	streamClient.Timeout = 0
	resp, err := streamClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("relay stream: status %d", resp.StatusCode)
	}
	c.set(true, nil)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 2*MaxWebhookBody)
	var data []byte
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case bytes.HasPrefix(line, []byte("data: ")):
			data = append(data[:0], line[len("data: "):]...)
		case len(line) == 0 && len(data) > 0:
			var e RelayEvent
			if err := json.Unmarshal(data, &e); err != nil {
				return fmt.Errorf("relay event: %w", err)
			}
			data = data[:0]
			code, msg := c.Ingest.Handle(ctx, "relay", Headers{Event: e.Event, Delivery: e.Delivery, Signature: e.Signature}, e.Body, time.Now())
			if code >= 500 {
				return fmt.Errorf("delivery %s: %s", e.Delivery, msg)
			}
			if err := c.ack(ctx, e.Delivery); err != nil {
				return err
			}
			c.mu.Lock()
			c.lastEvent = time.Now()
			c.mu.Unlock()
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return errors.New("relay closed the stream")
}

func (c *RelayClient) ack(ctx context.Context, delivery string) error {
	b, _ := json.Marshal(map[string]string{"delivery": delivery})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/relay/v1/ack", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("relay ack: status %d", resp.StatusCode)
	}
	return nil
}
