package auth_test

import (
	"net/http"
	"os/exec"
	"testing"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/auth"
	"github.com/alternayte/sayso/internal/testenv"
)

func TestSCN_AUTH_004_BootstrapAndGrant(t *testing.T) {
	e := apptest.New(t, apptest.Options{BootstrapAdmins: []string{"dev-bob"}})
	bob, _ := e.SignIn(t, "dev-bob")
	var me auth.MeBody
	apptest.JSON(t, bob, http.MethodGet, e.Base+"/api/v1/me", nil, &me)
	if me.Role != "admin" {
		t.Fatalf("dev-bob role %q, want admin", me.Role)
	}

	url := e.Deps.Pool.Config().ConnString()
	cmd := exec.Command(testenv.Binary(t), "admin", "grant", "--github-login", "dev-alice", "--role", "maintainer")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "SAYSO_DATABASE_URL=" + url}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("admin grant: %v: %s", err, out)
	}
	alice, _ := e.SignIn(t, "dev-alice")
	apptest.JSON(t, alice, http.MethodGet, e.Base+"/api/v1/me", nil, &me)
	if me.Role != "maintainer" {
		t.Fatalf("dev-alice role %q, want maintainer", me.Role)
	}
}
