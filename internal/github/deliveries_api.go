package github

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/audit"
)

// RemoteDeliveryBody is one delivery as GitHub lists it, with the state in Sayso.
type RemoteDeliveryBody struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	Event       string    `json:"event"`
	Action      string    `json:"action"`
	DeliveredAt time.Time `json:"delivered_at"`
	StatusCode  int       `json:"status_code"`
	Redelivery  bool      `json:"redelivery"`
	Failed      bool      `json:"failed"`
	// Handled is true when Sayso recorded the delivery.
	Handled bool `json:"handled"`
}

// DeliveriesAPI lists the recent App deliveries on GitHub and asks GitHub to
// send one again (REQ-CFG-008).
type DeliveriesAPI struct {
	Pool    *pgxpool.Pool
	Secrets SecretReader
	GitHub  func(ctx context.Context) (*Client, error)
}

func (a *DeliveriesAPI) client(ctx context.Context) (*Client, *App, error) {
	app, err := LoadApp(ctx, a.Pool, a.Secrets)
	if errors.Is(err, ErrNoApp) {
		return nil, nil, huma.Error409Conflict("the GitHub App does not exist")
	}
	if err != nil {
		return nil, nil, err
	}
	gh, err := a.GitHub(ctx)
	return gh, app, err
}

// Register adds the operations.
func (a *DeliveriesAPI) Register(api huma.API, admin map[string]any, actor func(context.Context) audit.Actor) {
	tags := []string{"github"}
	huma.Register(api, huma.Operation{OperationID: "list-github-deliveries", Method: http.MethodGet, Path: "/github/deliveries", Tags: tags, Metadata: admin},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []RemoteDeliveryBody }, error) {
			gh, app, err := a.client(ctx)
			if err != nil {
				return nil, err
			}
			all, err := gh.Deliveries(ctx, app, time.Now().Add(-DeliveryWindow))
			if err != nil {
				return nil, huma.Error502BadGateway("GitHub: " + err.Error())
			}
			if len(all) > 100 {
				all = all[:100]
			}
			guids := make([]string, 0, len(all))
			for _, d := range all {
				guids = append(guids, d.GUID)
			}
			known, err := New(a.Pool).KnownDeliveries(ctx, guids)
			if err != nil {
				return nil, err
			}
			handled := map[string]bool{}
			for _, k := range known {
				handled[k] = true
			}
			out := make([]RemoteDeliveryBody, 0, len(all))
			for _, d := range all {
				out = append(out, RemoteDeliveryBody{ID: d.ID, GUID: d.GUID, Event: d.Event, Action: d.Action, DeliveredAt: d.DeliveredAt,
					StatusCode: d.StatusCode, Redelivery: d.Redelivery, Failed: d.Failed(), Handled: handled[d.GUID]})
			}
			return &struct{ Body []RemoteDeliveryBody }{out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "redeliver-github-delivery", Method: http.MethodPost, Path: "/github/deliveries/{id}/redeliver", Tags: tags, Metadata: admin,
		DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, in *struct {
			ID   int64 `path:"id"`
			Body struct {
				GUID string `json:"guid"`
			}
		}) (*struct{}, error) {
			gh, app, err := a.client(ctx)
			if err != nil {
				return nil, err
			}
			if err := New(a.Pool).RequestRedelivery(ctx, in.Body.GUID); err != nil {
				return nil, err
			}
			if err := gh.Redeliver(ctx, app, in.ID); err != nil {
				return nil, huma.Error502BadGateway("GitHub: " + err.Error())
			}
			return nil, audit.Write(ctx, a.Pool, actor(ctx), "github.redelivery", in.Body.GUID, map[string]any{"delivery_id": in.ID})
		})
}
