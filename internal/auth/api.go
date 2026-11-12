package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/github"
)

// Access levels for Operation.Metadata["access"].
const (
	AccessPublic = "public"
	AccessSetup  = "setup"
)

// Op returns operation metadata that requires a minimum role.
func Op(min Role) map[string]any { return map[string]any{"access": string(min)} }

// PublicOp is metadata for an operation without sign-in.
func PublicOp() map[string]any { return map[string]any{"access": AccessPublic} }

// SetupOp is metadata for an operation of the setup flow.
func SetupOp() map[string]any { return map[string]any{"access": AccessSetup} }

// Enforce is the Huma middleware that applies each operation's access
// level (REQ-AUTH-005). An operation without the metadata is refused.
func Enforce(api huma.API, appExists func(context.Context) (bool, error)) func(huma.Context, func(huma.Context)) {
	return func(hc huma.Context, next func(huma.Context)) {
		ctx := hc.Context()
		access, _ := hc.Operation().Metadata["access"].(string)
		switch access {
		case AccessPublic:
			next(hc)
			return
		case AccessSetup:
			exists, err := appExists(ctx)
			if err != nil {
				_ = huma.WriteErr(api, hc, http.StatusInternalServerError, "database error")
				return
			}
			if exists {
				_ = huma.WriteErr(api, hc, http.StatusNotFound, "not found")
				return
			}
			if SetupFrom(ctx) || UserFrom(ctx) != nil && UserFrom(ctx).Role == Admin {
				next(hc)
				return
			}
			_ = huma.WriteErr(api, hc, http.StatusUnauthorized, "setup token required")
			return
		}
		min, err := ParseRole(access)
		if err != nil {
			_ = huma.WriteErr(api, hc, http.StatusInternalServerError, "operation has no access level")
			return
		}
		u := UserFrom(ctx)
		if u == nil {
			_ = huma.WriteErr(api, hc, http.StatusUnauthorized, "sign in required")
			return
		}
		if !u.Role.AtLeast(min) {
			_ = huma.WriteErr(api, hc, http.StatusForbidden, "your role cannot do this")
			return
		}
		next(hc)
	}
}

type MeBody struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

type SetupStateBody struct {
	AppExists    bool   `json:"app_exists"`
	SetupSession bool   `json:"setup_session"`
	SignedIn     bool   `json:"signed_in"`
	InstallURL   string `json:"install_url"`
}

type TeamRoleBody struct {
	TeamSlug string `json:"team_slug"`
	Role     string `json:"role" enum:"admin,maintainer,viewer"`
}

type GrantBody struct {
	Login     string `json:"login"`
	Role      string `json:"role" enum:"admin,maintainer,viewer"`
	GrantedBy string `json:"granted_by"`
}

type UserBody struct {
	Login       string     `json:"login"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	RoleSource  string     `json:"role_source"`
	Status      string     `json:"status"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

type AccessBody struct {
	Teams  []TeamRoleBody `json:"teams"`
	Grants []GrantBody    `json:"grants"`
	Users  []UserBody     `json:"users"`
}

// Register adds the auth operations.
func (s *Service) Register(api huma.API) {
	s.registerManifest(api)
	huma.Register(api, huma.Operation{
		OperationID: "get-me", Method: http.MethodGet, Path: "/me", Tags: []string{"auth"}, Metadata: Op(Viewer),
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body MeBody }, error) {
		u := UserFrom(ctx)
		return &struct{ Body MeBody }{MeBody{Login: u.Login, Name: u.Name, Role: string(u.Role)}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-setup-state", Method: http.MethodGet, Path: "/setup/state", Tags: []string{"setup"}, Metadata: PublicOp(),
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body SetupStateBody }, error) {
		exists, err := github.Exists(ctx, s.pool)
		if err != nil {
			return nil, err
		}
		install, err := s.installURL(ctx)
		if err != nil {
			return nil, err
		}
		return &struct{ Body SetupStateBody }{SetupStateBody{AppExists: exists, SetupSession: SetupFrom(ctx) && !exists, SignedIn: UserFrom(ctx) != nil, InstallURL: install}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "create-setup-session", Method: http.MethodPost, Path: "/setup/session", Tags: []string{"setup"}, Metadata: PublicOp(),
	}, func(ctx context.Context, in *struct {
		Body struct {
			Token string `json:"token"`
		}
	}) (*struct {
		SetCookie http.Cookie `header:"Set-Cookie"`
	}, error) {
		exists, err := github.Exists(ctx, s.pool)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, huma.Error404NotFound("not found")
		}
		c, err := s.SetupSession(in.Body.Token)
		if err != nil {
			_ = audit.Write(ctx, s.pool, audit.Actor{Kind: "user", Login: "anonymous"}, "setup.token_refused", "", nil)
			return nil, huma.Error401Unauthorized("wrong setup token")
		}
		return &struct {
			SetCookie http.Cookie `header:"Set-Cookie"`
		}{*c}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-access", Method: http.MethodGet, Path: "/access", Tags: []string{"access"}, Metadata: Op(Admin),
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body AccessBody }, error) {
		q := New(s.pool)
		out := AccessBody{Teams: []TeamRoleBody{}, Grants: []GrantBody{}, Users: []UserBody{}}
		teams, err := q.TeamRoles(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range teams {
			out.Teams = append(out.Teams, TeamRoleBody(t))
		}
		grants, err := q.Grants(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range grants {
			out.Grants = append(out.Grants, GrantBody{Login: g.Login, Role: g.Role, GrantedBy: g.GrantedBy})
		}
		users, err := q.Users(ctx)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			out.Users = append(out.Users, UserBody{Login: u.Login, Name: u.Name, Role: u.Role, RoleSource: u.RoleSource, Status: u.Status, LastLoginAt: u.LastLoginAt})
		}
		return &struct{ Body AccessBody }{out}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "put-team-role", Method: http.MethodPut, Path: "/access/teams/{slug}", Tags: []string{"access"}, Metadata: Op(Admin),
	}, func(ctx context.Context, in *struct {
		Slug string `path:"slug"`
		Body struct {
			Role string `json:"role" enum:"admin,maintainer,viewer"`
		}
	}) (*struct{}, error) {
		u := UserFrom(ctx)
		if err := New(s.pool).PutTeamRole(ctx, PutTeamRoleParams{TeamSlug: in.Slug, Role: in.Body.Role}); err != nil {
			return nil, err
		}
		return nil, audit.Write(ctx, s.pool, u.Actor(), "access.team_role", in.Slug, map[string]any{"role": in.Body.Role})
	})

	huma.Register(api, huma.Operation{
		OperationID: "delete-team-role", Method: http.MethodDelete, Path: "/access/teams/{slug}", Tags: []string{"access"}, Metadata: Op(Admin),
	}, func(ctx context.Context, in *struct {
		Slug string `path:"slug"`
	}) (*struct{}, error) {
		if err := New(s.pool).DeleteTeamRole(ctx, in.Slug); err != nil {
			return nil, err
		}
		return nil, audit.Write(ctx, s.pool, UserFrom(ctx).Actor(), "access.team_role_delete", in.Slug, nil)
	})

	huma.Register(api, huma.Operation{
		OperationID: "put-grant", Method: http.MethodPut, Path: "/access/grants/{login}", Tags: []string{"access"}, Metadata: Op(Admin),
	}, func(ctx context.Context, in *struct {
		Login string `path:"login"`
		Body  struct {
			Role string `json:"role" enum:"admin,maintainer,viewer"`
		}
	}) (*struct{}, error) {
		return nil, s.Grant(ctx, UserFrom(ctx).Actor(), in.Login, Role(in.Body.Role))
	})

	huma.Register(api, huma.Operation{
		OperationID: "delete-grant", Method: http.MethodDelete, Path: "/access/grants/{login}", Tags: []string{"access"}, Metadata: Op(Admin),
	}, func(ctx context.Context, in *struct {
		Login string `path:"login"`
	}) (*struct{}, error) {
		if err := New(s.pool).DeleteGrant(ctx, in.Login); err != nil {
			return nil, err
		}
		return nil, audit.Write(ctx, s.pool, UserFrom(ctx).Actor(), "access.grant_delete", in.Login, nil)
	})
}
