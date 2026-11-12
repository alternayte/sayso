package settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/auth"
)

type SettingBody struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	Default   json.RawMessage `json:"default"`
	Revision  int32           `json:"revision"`
	UpdatedBy string          `json:"updated_by"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type HistoryBody struct {
	Revision  int32           `json:"revision"`
	OldValue  json.RawMessage `json:"old_value"`
	NewValue  json.RawMessage `json:"new_value"`
	UpdatedBy string          `json:"updated_by"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func body(s Setting) SettingBody {
	return SettingBody{Key: s.Key, Value: s.Value, Default: Defaults[s.Key], Revision: s.Revision, UpdatedBy: s.UpdatedBy, UpdatedAt: s.UpdatedAt}
}

// Register adds the settings operations. Only admins read and change them.
func (s *Store) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-settings", Method: http.MethodGet, Path: "/settings", Tags: []string{"settings"}, Metadata: auth.Op(auth.Admin),
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body []SettingBody }, error) {
		rows, err := s.All(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]SettingBody, 0, len(rows))
		for _, r := range rows {
			out = append(out, body(r))
		}
		return &struct{ Body []SettingBody }{out}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "put-setting", Method: http.MethodPut, Path: "/settings/{key}", Tags: []string{"settings"}, Metadata: auth.Op(auth.Admin),
	}, func(ctx context.Context, in *struct {
		Key  string `path:"key"`
		Body struct {
			Value json.RawMessage `json:"value"`
		}
	}) (*struct{ Body SettingBody }, error) {
		row, err := s.Set(ctx, auth.UserFrom(ctx).Actor(), in.Key, in.Body.Value)
		if errors.Is(err, ErrUnknownKey) {
			return nil, huma.Error404NotFound(err.Error())
		}
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		return &struct{ Body SettingBody }{body(row)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-setting-history", Method: http.MethodGet, Path: "/settings/{key}/history", Tags: []string{"settings"}, Metadata: auth.Op(auth.Admin),
	}, func(ctx context.Context, in *struct {
		Key string `path:"key"`
	}) (*struct{ Body []HistoryBody }, error) {
		if _, err := s.Get(ctx, in.Key); errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("unknown setting")
		}
		rows, err := s.History(ctx, in.Key)
		if err != nil {
			return nil, err
		}
		out := make([]HistoryBody, 0, len(rows))
		for _, r := range rows {
			out = append(out, HistoryBody{Revision: r.Revision, OldValue: r.OldValue, NewValue: r.NewValue, UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt})
		}
		return &struct{ Body []HistoryBody }{out}, nil
	})
}
