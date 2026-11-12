package github

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TokenGrant is what an installation token request asked for.
type TokenGrant struct {
	Repositories []string          `json:"repositories,omitempty"`
	Permissions  map[string]string `json:"permissions,omitempty"`
	At           time.Time         `json:"-"`
}

// Grants returns the recorded installation token requests, oldest first.
func (f *Fake) Grants() []TokenGrant {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []TokenGrant
	for _, g := range f.instToks {
		out = append(out, g)
	}
	sortGrants(out)
	return out
}

func sortGrants(g []TokenGrant) {
	for i := 1; i < len(g); i++ {
		for j := i; j > 0 && g[j].At.Before(g[j-1].At); j-- {
			g[j], g[j-1] = g[j-1], g[j]
		}
	}
}

type manifest struct {
	Name        string `json:"name"`
	RedirectURL string `json:"redirect_url"`
	HookAttrs   struct {
		URL string `json:"url"`
	} `json:"hook_attributes"`
	CallbackURLs []string          `json:"callback_urls"`
	Permissions  map[string]string `json:"default_permissions"`
	Events       []string          `json:"default_events"`
	org          string
}

// Delivery is one webhook delivery that the fake sent.
type Delivery struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	DeliveredAt time.Time `json:"delivered_at"`
	Redelivery  bool      `json:"redelivery"`
	Status      string    `json:"status"`
	StatusCode  int       `json:"status_code"`
	Event       string    `json:"event"`
	Action      string    `json:"action"`
	body        []byte
}

// LastManifest returns the manifest of the last App creation.
func (f *Fake) LastManifest() (perms map[string]string, events []string, hook string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.manifests {
		return m.Permissions, m.Events, m.HookAttrs.URL
	}
	return nil, nil, ""
}

func (f *Fake) appRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /organizations/{org}/settings/apps/new", f.newApp)
	mux.HandleFunc("POST /organizations/{org}/settings/apps/new", f.newApp)
	mux.HandleFunc("POST /app-manifests/{code}/conversions", f.convert)
	mux.HandleFunc("GET /app/hook/deliveries", f.listDeliveries)
	mux.HandleFunc("POST /app/hook/deliveries/{id}/attempts", f.redeliver)
	mux.HandleFunc("GET /apps/{slug}/installations/new", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Install " + r.PathValue("slug")))
	})
}

// newApp stands for the GitHub page that creates an App from a manifest.
// The fake confirms at once and redirects with a code.
func (f *Fake) newApp(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	var m manifest
	if err := json.Unmarshal([]byte(r.Form.Get("manifest")), &m); err != nil || m.RedirectURL == "" {
		http.Error(w, "manifest missing or invalid", http.StatusUnprocessableEntity)
		return
	}
	m.org = r.PathValue("org")
	if m.org != f.Org {
		http.Error(w, "unknown organization", http.StatusNotFound)
		return
	}
	code := token("mc_")
	f.mu.Lock()
	f.manifests[code] = m
	f.mu.Unlock()
	u, err := url.Parse(m.RedirectURL)
	if err != nil {
		http.Error(w, "bad redirect_url", http.StatusUnprocessableEntity)
		return
	}
	q := u.Query()
	q.Set("code", code)
	q.Set("state", r.URL.Query().Get("state"))
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (f *Fake) convert(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	m, ok := f.manifests[r.PathValue("code")]
	f.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	secret := token("whsec_")
	f.mu.Lock()
	f.HookURL, f.WebhookSecret = m.HookAttrs.URL, secret
	slug, id, cid, csec := f.AppSlug, f.AppID, f.ClientID, f.Secret
	base := "http://" + r.Host
	f.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "slug": slug, "name": m.Name, "html_url": base + "/apps/" + slug,
		"client_id": cid, "client_secret": csec, "webhook_secret": secret, "pem": pemKey,
		"owner": map[string]any{"login": m.org},
	})
}

// SetHook points the webhook sender at url with secret. The manifest flow
// sets both; tests that skip it call SetHook.
func (f *Fake) SetHook(url, secret string) {
	f.mu.Lock()
	f.HookURL, f.WebhookSecret = url, secret
	f.mu.Unlock()
}

// Send signs a webhook payload, sends it to the hook URL and records the
// delivery. It returns the delivery GUID and the response status (0 when
// the connection failed).
func (f *Fake) Send(event string, payload map[string]any) (string, int) {
	f.mu.Lock()
	if _, ok := payload["installation"]; !ok && event != "ping" {
		payload["installation"] = map[string]any{"id": f.InstallID, "account": map[string]any{"login": f.Org}}
	}
	f.nextDelivery++
	d := &Delivery{ID: f.nextDelivery, GUID: newGUID(), Event: event}
	d.Action, _ = payload["action"].(string)
	f.mu.Unlock()
	body, _ := json.Marshal(payload)
	d.body = body
	f.post(d, false)
	return d.GUID, d.StatusCode
}

func (f *Fake) post(d *Delivery, redelivery bool) {
	f.mu.Lock()
	hook, secret := f.HookURL, f.WebhookSecret
	rec := *d
	rec.Redelivery = redelivery
	rec.DeliveredAt = time.Now()
	if redelivery {
		f.nextDelivery++
		rec.ID = f.nextDelivery
	}
	f.mu.Unlock()
	code := 0
	if hook != "" {
		req, err := http.NewRequest(http.MethodPost, hook, bytes.NewReader(d.body))
		if err == nil {
			m := hmac.New(sha256.New, []byte(secret))
			m.Write(d.body)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-GitHub-Event", d.Event)
			req.Header.Set("X-GitHub-Delivery", d.GUID)
			req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(m.Sum(nil)))
			req.Header.Set("User-Agent", "GitHub-Hookshot/fake")
			if resp, err := f.httpc.Do(req); err == nil {
				code = resp.StatusCode
				resp.Body.Close()
			}
		}
	}
	rec.StatusCode = code
	rec.Status = "OK"
	if code == 0 || code >= 400 {
		rec.Status = "failed"
	}
	f.mu.Lock()
	f.deliveries = append(f.deliveries, &rec)
	d.StatusCode, d.Status = rec.StatusCode, rec.Status
	f.mu.Unlock()
}

// Deliveries returns every delivery attempt, oldest first.
func (f *Fake) Deliveries() []Delivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Delivery, 0, len(f.deliveries))
	for _, d := range f.deliveries {
		out = append(out, *d)
	}
	return out
}

func newGUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (f *Fake) appAuthorized(r *http.Request) bool {
	// App JWTs have three dot-separated parts.
	return strings.Count(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".") == 2
}

func (f *Fake) listDeliveries(w http.ResponseWriter, r *http.Request) {
	if !f.appAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "A JSON web token could not be decoded"})
		return
	}
	f.mu.Lock()
	out := make([]Delivery, 0, len(f.deliveries))
	for i := len(f.deliveries) - 1; i >= 0; i-- {
		out = append(out, *f.deliveries[i])
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) redeliver(w http.ResponseWriter, r *http.Request) {
	if !f.appAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "A JSON web token could not be decoded"})
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f.mu.Lock()
	var src *Delivery
	for _, d := range f.deliveries {
		if d.ID == id {
			src = d
		}
	}
	var orig []byte
	if src != nil {
		for _, d := range f.deliveries {
			if d.GUID == src.GUID && d.body != nil {
				orig = d.body
			}
		}
	}
	f.mu.Unlock()
	if src == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	cp := *src
	cp.body = orig
	go f.post(&cp, true)
	writeJSON(w, http.StatusAccepted, map[string]any{})
}
