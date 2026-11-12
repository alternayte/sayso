//go:build contract

package auth_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/auth"
)

func sessionCookie(t *testing.T, e *apptest.Env, c *http.Client) *http.Cookie {
	t.Helper()
	u, _ := url.Parse(e.Base)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == auth.SessionCookie {
			return ck
		}
	}
	return nil
}

func TestSCN_AUTH_002_SignInMembershipCookies(t *testing.T) {
	e := apptest.New(t, apptest.Options{})

	c, where := e.SignIn(t, "dev-alice")
	if where != "/" {
		t.Fatalf("dev-alice landed on %q", where)
	}
	var me auth.MeBody
	if code := apptest.JSON(t, c, http.MethodGet, e.Base+"/api/v1/me", nil, &me); code != 200 || me.Login != "dev-alice" {
		t.Fatalf("me = %d %+v", code, me)
	}

	// Inspect the raw Set-Cookie flags of a fresh sign-in.
	e.GitHub.SignInAs("dev-alice")
	raw := e.Client(t)
	raw.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Path == "/" {
			return http.ErrUseLastResponse
		}
		return nil
	}
	resp, err := raw.Get(e.Base + "/auth/github/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var flags *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.SessionCookie {
			flags = ck
		}
	}
	if flags == nil || !flags.HttpOnly || flags.SameSite != http.SameSiteLaxMode || flags.Secure {
		t.Fatalf("session cookie flags on http: %+v", flags)
	}

	_, where = e.SignIn(t, "mallory")
	if where != "/signin?error=not_member" {
		t.Fatalf("mallory landed on %q", where)
	}
	if rows, _ := audit.Search(t.Context(), e.Deps.Pool, audit.Query{Action: "auth.signin_refused"}); len(rows) != 1 {
		t.Fatalf("refused sign-in audit rows: %d", len(rows))
	}

	// A cookie POST from a foreign origin is refused.
	req, _ := http.NewRequest(http.MethodPost, e.Base+"/auth/logout", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(sessionCookie(t, e, c))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin POST = %d, want 403", resp.StatusCode)
	}
}

func TestSCN_AUTH_002_SecureCookieOnHTTPS(t *testing.T) {
	// The Secure flag follows the scheme of the base URL.
	svc := auth.NewService(nil, auth.Config{BaseURL: "https://sayso.acme.test", SigningKey: []byte("k"), SetupToken: "tok"}, nil, nil, nil)
	c, err := svc.SetupSession("tok")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie on https: %+v", c)
	}
}

func TestSCN_AUTH_003_RolesFromTeamsAndGrants(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	ctx := t.Context()
	if err := e.F.Auth.Grant(ctx, audit.System, "dev-alice", auth.Viewer); err != nil {
		t.Fatal(err)
	}
	c, _ := e.SignIn(t, "octo-admin")
	var me auth.MeBody
	// No team mapping yet: octo-admin is a member with the default role.
	apptest.JSON(t, c, http.MethodGet, e.Base+"/api/v1/me", nil, &me)
	if me.Role != "viewer" {
		t.Fatalf("before mapping: role %q", me.Role)
	}
	if code := apptest.JSON(t, c, http.MethodPut, e.Base+"/api/v1/access/teams/platform", map[string]string{"role": "admin"}, nil); code != http.StatusForbidden {
		t.Fatalf("viewer mapped a team: %d", code)
	}
	if _, err := e.Deps.Pool.Exec(ctx, `INSERT INTO team_roles (team_slug, role) VALUES ('platform', 'admin')`); err != nil {
		t.Fatal(err)
	}
	c, _ = e.SignIn(t, "octo-admin")
	apptest.JSON(t, c, http.MethodGet, e.Base+"/api/v1/me", nil, &me)
	if me.Role != "admin" {
		t.Fatalf("octo-admin role %q, want admin", me.Role)
	}
	if code := apptest.JSON(t, c, http.MethodGet, e.Base+"/api/v1/access", nil, nil); code != 200 {
		t.Fatalf("admin access page: %d", code)
	}
	alice, _ := e.SignIn(t, "dev-alice")
	apptest.JSON(t, alice, http.MethodGet, e.Base+"/api/v1/me", nil, &me)
	if me.Role != "viewer" {
		t.Fatalf("dev-alice role %q, want viewer", me.Role)
	}

	e.GitHub.SetTeamMember("platform", "octo-admin", false)
	e.Clock.Advance(14 * time.Minute)
	if code := apptest.JSON(t, c, http.MethodGet, e.Base+"/api/v1/access", nil, nil); code != 200 {
		t.Fatalf("before the recheck the role stays: %d", code)
	}
	e.Clock.Advance(2 * time.Minute)
	if code := apptest.JSON(t, c, http.MethodGet, e.Base+"/api/v1/access", nil, nil); code != http.StatusForbidden {
		t.Fatalf("after 15 minutes the admin route = %d, want 403", code)
	}

	e.GitHub.SetOrgMember("octo-admin", false)
	e.Clock.Advance(16 * time.Minute)
	if code := apptest.JSON(t, c, http.MethodGet, e.Base+"/api/v1/me", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("after leaving the organization /me = %d, want 401", code)
	}
}
