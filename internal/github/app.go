package github

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNoApp means the GitHub App does not exist yet.
var ErrNoApp = errors.New("the GitHub App does not exist yet")

// SecretReader returns the plaintext of a stored secret by ID.
type SecretReader interface {
	ValueByID(ctx context.Context, id [16]byte) (string, error)
}

// Exists reports whether the GitHub App was created.
func Exists(ctx context.Context, db DBTX) (bool, error) {
	return New(db).AppExists(ctx)
}

// LoadApp reads the App row and its secrets.
func LoadApp(ctx context.Context, db DBTX, sec SecretReader) (*App, error) {
	row, err := New(db).GetApp(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoApp
	}
	if err != nil {
		return nil, err
	}
	app := &App{AppID: row.AppID, Slug: row.Slug, Org: row.OrgLogin, ClientID: row.ClientID}
	if app.ClientSecret, err = sec.ValueByID(ctx, row.ClientSecretID); err != nil {
		return nil, fmt.Errorf("App client secret: %w", err)
	}
	if app.PrivateKeyPEM, err = sec.ValueByID(ctx, row.PrivateKeyID); err != nil {
		return nil, fmt.Errorf("App private key: %w", err)
	}
	return app, nil
}

// SecretWriter stores a secret value and returns its ID.
type SecretWriter interface {
	PutValue(ctx context.Context, name, value string) ([16]byte, error)
}

// SaveApp stores the App and its credentials as secrets (REQ-GH-001).
func SaveApp(ctx context.Context, db DBTX, sec SecretWriter, app *App, webhookSecret, htmlURL string) error {
	ids := map[string][16]byte{}
	for name, v := range map[string]string{
		"github.client_secret": app.ClientSecret, "github.private_key": app.PrivateKeyPEM, "github.webhook_secret": webhookSecret,
	} {
		id, err := sec.PutValue(ctx, name, v)
		if err != nil {
			return err
		}
		ids[name] = id
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	return New(db).InsertApp(ctx, InsertAppParams{
		ID: id, AppID: app.AppID, Slug: app.Slug, OrgLogin: app.Org, ClientID: app.ClientID,
		ClientSecretID: ids["github.client_secret"], PrivateKeyID: ids["github.private_key"],
		WebhookSecretID: ids["github.webhook_secret"], HtmlUrl: htmlURL,
	})
}
