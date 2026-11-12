package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

type EntryBody struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	ActorKind  string          `json:"actor_kind"`
	ActorLogin string          `json:"actor_login"`
	Action     string          `json:"action"`
	Target     string          `json:"target"`
	Details    json.RawMessage `json:"details"`
}

// Register adds the read-only audit operations. The API has no operation
// that changes or deletes an entry (SI-13). meta is the admin-only access
// metadata from the auth package.
func Register(api huma.API, db DBTX, meta map[string]any) {
	huma.Register(api, huma.Operation{
		OperationID: "list-audit", Method: http.MethodGet, Path: "/audit", Tags: []string{"audit"}, Metadata: meta,
	}, func(ctx context.Context, in *struct {
		Action string `query:"action"`
		Before int64  `query:"before"`
		Limit  int32  `query:"limit" minimum:"1" maximum:"500" default:"100"`
	}) (*struct{ Body []EntryBody }, error) {
		rows, err := Search(ctx, db, Query{Action: in.Action, BeforeID: in.Before, Limit: in.Limit})
		if err != nil {
			return nil, err
		}
		out := make([]EntryBody, 0, len(rows))
		for _, r := range rows {
			out = append(out, EntryBody{ID: r.ID, At: r.At, ActorKind: r.ActorKind, ActorLogin: r.ActorLogin, Action: r.Action, Target: r.Target, Details: r.Details})
		}
		return &struct{ Body []EntryBody }{out}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-audit-actions", Method: http.MethodGet, Path: "/audit/actions", Tags: []string{"audit"}, Metadata: meta,
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body []string }, error) {
		a, err := New(db).Actions(ctx)
		if a == nil {
			a = []string{}
		}
		return &struct{ Body []string }{a}, err
	})
}
