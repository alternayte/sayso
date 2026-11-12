package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
)

// Permissions and events of the App manifest (§6.1).
var (
	ManifestPermissions = map[string]string{
		"contents": "write", "checks": "write", "pull_requests": "write", "issues": "write",
		"metadata": "read", "members": "read",
	}
	ManifestEvents = []string{
		"pull_request", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread",
		"issue_comment", "check_run",
	}
)

var orgName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// ManifestForm is what the setup page posts to GitHub.
type ManifestForm struct {
	ActionURL string `json:"action_url"`
	Manifest  string `json:"manifest"`
}

// BuildManifest returns the form that creates the App in an organization.
// GitHub sends installation events to every App, so the manifest does not
// list them.
func BuildManifest(webURL, baseURL, webhookURL, org, state string) (ManifestForm, error) {
	if !orgName.MatchString(org) {
		return ManifestForm{}, fmt.Errorf("%q is not a GitHub organization name", org)
	}
	if webhookURL == "" {
		webhookURL = baseURL + "/webhooks/github"
	}
	m := map[string]any{
		"name":                "Sayso " + org,
		"url":                 baseURL,
		"hook_attributes":     map[string]any{"url": webhookURL, "active": true},
		"redirect_url":        baseURL + "/setup/github/callback",
		"callback_urls":       []string{baseURL + "/auth/github/callback"},
		"public":              false,
		"default_permissions": ManifestPermissions,
		"default_events":      ManifestEvents,
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ManifestForm{}, err
	}
	action := strings.TrimRight(webURL, "/") + "/organizations/" + url.PathEscape(org) + "/settings/apps/new?state=" + url.QueryEscape(state)
	return ManifestForm{ActionURL: action, Manifest: string(b)}, nil
}

// InstallURL is where an admin installs the App on repositories.
func InstallURL(htmlURL string) string { return strings.TrimRight(htmlURL, "/") + "/installations/new" }

// CompleteManifest exchanges the code, stores the App and its credentials
// as secrets and writes an audit entry (REQ-GH-001).
func CompleteManifest(ctx context.Context, pool *pgxpool.Pool, gh *Client, sec SecretWriter, code string) (*App, string, error) {
	conv, err := gh.ConvertManifest(ctx, code)
	if err != nil {
		return nil, "", err
	}
	app := &App{AppID: conv.ID, Slug: conv.Slug, Org: conv.Owner.Login, ClientID: conv.ClientID, ClientSecret: conv.ClientSecret, PrivateKeyPEM: conv.PEM}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := SaveApp(ctx, tx, sec, app, conv.WebhookSecret, conv.HTMLURL); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Actor{Kind: "user", Login: "setup"}, "github.app_created", conv.Slug,
			map[string]any{"app_id": conv.ID, "org": conv.Owner.Login})
	})
	if err != nil {
		return nil, "", err
	}
	return app, InstallURL(conv.HTMLURL), nil
}
