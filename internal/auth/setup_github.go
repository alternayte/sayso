package auth

import (
	"context"
	"crypto/hmac"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/github"
)

const manifestStateCookie = "sayso_manifest_state"

type ManifestBody struct {
	Org string `json:"org" minLength:"1" maxLength:"39"`
}

// registerManifest adds the operation that starts the App manifest flow.
func (s *Service) registerManifest(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "create-app-manifest", Method: http.MethodPost, Path: "/setup/github/manifest", Tags: []string{"setup"}, Metadata: SetupOp(),
	}, func(ctx context.Context, in *struct{ Body ManifestBody }) (*struct {
		SetCookie http.Cookie `header:"Set-Cookie"`
		Body      github.ManifestForm
	}, error) {
		gh, err := s.GitHub(ctx)
		if err != nil {
			return nil, err
		}
		state, _, err := randomToken()
		if err != nil {
			return nil, err
		}
		form, err := github.BuildManifest(gh.WebURL, s.cfg.BaseURL, s.cfg.WebhookURL, in.Body.Org, state)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		return &struct {
			SetCookie http.Cookie `header:"Set-Cookie"`
			Body      github.ManifestForm
		}{*s.cookie(manifestStateCookie, state, 30*time.Minute), form}, nil
	})
}

// ManifestCallback is GET /setup/github/callback. GitHub redirects here with
// the code of the new App. It needs the setup session and works only while
// no App exists (SI-14).
func (s *Service) ManifestCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	exists, err := github.Exists(ctx, s.pool)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if exists || !SetupFrom(ctx) {
		http.NotFound(w, r)
		return
	}
	st, err := r.Cookie(manifestStateCookie)
	if err != nil || st.Value == "" || !hmac.Equal([]byte(st.Value), []byte(r.URL.Query().Get("state"))) {
		http.Redirect(w, r, "/setup?error=bad_state", http.StatusFound)
		return
	}
	http.SetCookie(w, s.cookie(manifestStateCookie, "", -1))
	gh, err := s.GitHub(ctx)
	if err != nil {
		s.log.Error("manifest callback", "error", err)
		http.Redirect(w, r, "/setup?error=github", http.StatusFound)
		return
	}
	if _, _, err := github.CompleteManifest(ctx, s.pool, gh, s.secrets, r.URL.Query().Get("code")); err != nil {
		s.log.Error("manifest conversion failed", "error", err)
		http.Redirect(w, r, "/setup?error=github", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/signin?setup=app_created", http.StatusFound)
}

// installURL returns the install link of the App, or "" before it exists.
func (s *Service) installURL(ctx context.Context) (string, error) {
	row, err := github.New(s.pool).GetApp(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return github.InstallURL(row.HtmlUrl), nil
}
