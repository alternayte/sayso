// Package github is the GitHub App client: App credentials, installation
// tokens, user sign-in and organization and team membership.
package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// App holds the credentials of the GitHub App.
type App struct {
	AppID         int64
	Slug          string
	Org           string
	ClientID      string
	ClientSecret  string
	PrivateKeyPEM string
}

// Client calls the GitHub web and REST endpoints.
type Client struct {
	WebURL string
	APIURL string
	HTTP   *http.Client
	Now    func() time.Time
	// Limits is shared by all clients of a process. Nil means no tracking.
	Limits *RateLimits
}

// User is a GitHub account.
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

// ErrNotFound is a 404 from GitHub.
var ErrNotFound = errors.New("github: not found")

// AuthorizeURL is where a person signs in with the App's user authorization.
func (c *Client) AuthorizeURL(app *App, redirect, state string) string {
	v := url.Values{"client_id": {app.ClientID}, "redirect_uri": {redirect}, "state": {state}}
	return strings.TrimRight(c.WebURL, "/") + "/login/oauth/authorize?" + v.Encode()
}

// ExchangeCode trades an OAuth code for a user token.
func (c *Client) ExchangeCode(ctx context.Context, app *App, code, redirect string) (string, error) {
	form := url.Values{"client_id": {app.ClientID}, "client_secret": {app.ClientSecret}, "code": {code}, "redirect_uri": {redirect}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.WebURL, "/")+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := c.do(req, &out); err != nil {
		return "", err
	}
	if out.Error != "" || out.AccessToken == "" {
		return "", fmt.Errorf("github: code exchange failed: %s", out.Error)
	}
	return out.AccessToken, nil
}

// CurrentUser returns the account of a user token.
func (c *Client) CurrentUser(ctx context.Context, userToken string) (User, error) {
	var u User
	err := c.api(ctx, http.MethodGet, "/user", "Bearer "+userToken, &u)
	return u, err
}

// IsOrgMember checks organization membership with any token that can read it.
func (c *Client) IsOrgMember(ctx context.Context, token, org, login string) (bool, error) {
	err := c.api(ctx, http.MethodGet, "/orgs/"+url.PathEscape(org)+"/members/"+url.PathEscape(login), "Bearer "+token, nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// IsTeamMember checks an active team membership with an installation token.
func (c *Client) IsTeamMember(ctx context.Context, token, org, team, login string) (bool, error) {
	var out struct {
		State string `json:"state"`
	}
	err := c.api(ctx, http.MethodGet, "/orgs/"+url.PathEscape(org)+"/teams/"+url.PathEscape(team)+"/memberships/"+url.PathEscape(login), "Bearer "+token, &out)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil && out.State == "active", err
}

// OrgInstallationToken mints an installation token for the App's
// organization installation. Callers mint one per activity (§6.1).
func (c *Client) OrgInstallationToken(ctx context.Context, app *App) (string, error) {
	jwt, err := c.appJWT(app)
	if err != nil {
		return "", err
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	if err := c.api(ctx, http.MethodGet, "/orgs/"+url.PathEscape(app.Org)+"/installation", "Bearer "+jwt, &inst); err != nil {
		return "", fmt.Errorf("github: find installation: %w", err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if err := c.api(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", inst.ID), "Bearer "+jwt, &tok); err != nil {
		return "", fmt.Errorf("github: installation token: %w", err)
	}
	return tok.Token, nil
}

func (c *Client) appJWT(app *App) (string, error) {
	block, _ := pem.Decode([]byte(app.PrivateKeyPEM))
	if block == nil {
		return "", fmt.Errorf("github: App private key is not PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("github: App private key is not RSA")
		}
		key = rk
	} else {
		return "", fmt.Errorf("github: parse App private key: %w", err)
	}
	now := c.now()
	enc := base64.RawURLEncoding
	head := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": app.AppID})
	if err != nil {
		return "", err
	}
	signing := head + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) api(ctx context.Context, method, path, auth string, out any) error {
	return c.call(ctx, call{method: method, path: path, auth: auth, out: out})
}

// call is one REST request. inst keys the rate limit; 0 means a call
// without an installation (App JWT or user token).
type call struct {
	method, path, auth string
	in, out            any
	inst               int64
	prio               Priority
	onResponse         func(*http.Response)
}

func (c *Client) call(ctx context.Context, k call) error {
	if err := c.Limits.wait(ctx, k.inst, k.prio); err != nil {
		return err
	}
	var body io.Reader
	if k.in != nil {
		b, err := json.Marshal(k.in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, k.method, strings.TrimRight(c.APIURL, "/")+k.path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", k.auth)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if k.in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.doLimited(req, k)
}

func (c *Client) do(req *http.Request, out any) error { return c.doLimited(req, call{out: out}) }

func (c *Client) doLimited(req *http.Request, k call) error {
	out := k.out
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	c.Limits.observe(k.inst, resp)
	if k.onResponse != nil {
		k.onResponse(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode >= 300:
		return &StatusError{Method: req.Method, Path: req.URL.Path, Code: resp.StatusCode, Body: truncate(body, 200)}
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}

// StatusError is a GitHub response with a status of 300 or more.
type StatusError struct {
	Method, Path string
	Code         int
	Body         string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("github: %s %s: status %d: %s", e.Method, e.Path, e.Code, e.Body)
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}
