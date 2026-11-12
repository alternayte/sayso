package github

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AppBody struct {
	AppID      int64  `json:"app_id"`
	Slug       string `json:"slug"`
	Org        string `json:"org"`
	HTMLURL    string `json:"html_url"`
	InstallURL string `json:"install_url"`
}

type InstallationBody struct {
	ID           int64      `json:"id"`
	AccountLogin string     `json:"account_login"`
	SuspendedAt  *time.Time `json:"suspended_at"`
}

type DeliveryBody struct {
	DeliveryID  string     `json:"delivery_id"`
	Event       string     `json:"event"`
	Action      string     `json:"action"`
	ReceivedVia string     `json:"received_via"`
	ReceivedAt  time.Time  `json:"received_at"`
	HandledAt   *time.Time `json:"handled_at"`
	Error       string     `json:"error"`
}

type IngressBody struct {
	Mode       string `json:"mode" enum:"direct,relay"`
	WebhookURL string `json:"webhook_url"`
	// Relay stream state; empty in direct mode.
	RelayConnected bool   `json:"relay_connected"`
	RelayError     string `json:"relay_error"`
}

type GitHubBody struct {
	App           *AppBody           `json:"app"`
	Installations []InstallationBody `json:"installations"`
	Ingress       IngressBody        `json:"ingress"`
	Deliveries    []DeliveryBody     `json:"deliveries"`
}

// IngressInfo describes the configured webhook ingress.
type IngressInfo struct {
	Mode       string
	WebhookURL string
	Relay      *RelayClient
}

// Register adds the GitHub settings operation for admins.
func Register(api huma.API, pool *pgxpool.Pool, meta map[string]any, ingress IngressInfo) {
	huma.Register(api, huma.Operation{
		OperationID: "get-github", Method: http.MethodGet, Path: "/github", Tags: []string{"github"}, Metadata: meta,
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body GitHubBody }, error) {
		q := New(pool)
		out := GitHubBody{Installations: []InstallationBody{}, Deliveries: []DeliveryBody{}, Ingress: IngressBody{Mode: ingress.Mode, WebhookURL: ingress.WebhookURL}}
		row, err := q.GetApp(ctx)
		switch {
		case err == nil:
			out.App = &AppBody{AppID: row.AppID, Slug: row.Slug, Org: row.OrgLogin, HTMLURL: row.HtmlUrl, InstallURL: InstallURL(row.HtmlUrl)}
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, err
		}
		insts, err := q.Installations(ctx)
		if err != nil {
			return nil, err
		}
		for _, i := range insts {
			out.Installations = append(out.Installations, InstallationBody{ID: i.InstallationID, AccountLogin: i.AccountLogin, SuspendedAt: i.SuspendedAt})
		}
		ds, err := q.RecentDeliveries(ctx, 50)
		if err != nil {
			return nil, err
		}
		for _, d := range ds {
			out.Deliveries = append(out.Deliveries, DeliveryBody(d))
		}
		if ingress.Relay != nil {
			st := ingress.Relay.State()
			out.Ingress.RelayConnected, out.Ingress.RelayError = st.Connected, st.LastError
		}
		return &struct{ Body GitHubBody }{out}, nil
	})
}
