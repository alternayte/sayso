package skills_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/secrets"
	"github.com/alternayte/sayso/internal/skills"
)

func TestSCN_SKL_005_ExtensionOnlyWhereEnabled(t *testing.T) {
	e := apptest.New(t, apptest.Options{BootstrapAdmins: []string{"octo-admin"}})
	admin, _ := e.SignIn(t, "octo-admin")
	const secret = "lint-helper-secret-0009"
	in := skills.ExtensionInput{Spec: "acme-pi-lint@1.4.0", Env: map[string]string{"LINT_LEVEL": "strict"},
		Secrets: map[string]*secrets.Input{"LINT_TOKEN": {Value: secret}}, Enabled: true, Repositories: []string{"acme/web"}}
	if code := apptest.JSON(t, admin, http.MethodPut, e.Base+"/api/v1/extensions/lint-helper", in, nil); code != 200 {
		t.Fatalf("put extension: %d", code)
	}
	if code := apptest.JSON(t, admin, http.MethodPut, e.Base+"/api/v1/extensions/unpinned", skills.ExtensionInput{Spec: "acme-pi-lint@latest", Enabled: true}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("unpinned extension: %d", code)
	}
	x := e.F.Extensions
	spec := func(repo string) string {
		names, err := x.Enabled(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		exts, err := x.Spec(context.Background(), names)
		if err != nil {
			t.Fatal(err)
		}
		_, tok, err := runner.Create(context.Background(), e.Deps.Pool, runner.NewRun{Kind: "noop", Spec: runner.Spec{Extensions: exts}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodGet, e.Base+"/api/runner/v1/spec", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	pay := spec("acme/payments")
	if strings.Contains(pay, "acme-pi-lint") || strings.Contains(pay, secret) {
		t.Fatalf("acme/payments spec holds the extension: %s", pay)
	}
	web := spec("acme/web")
	var s runner.Spec
	_ = json.Unmarshal([]byte(web), &s)
	if len(s.Extensions) != 1 || s.Extensions[0].Package != "acme-pi-lint@1.4.0" || s.Extensions[0].Env["LINT_TOKEN"] != secret || s.Extensions[0].Env["LINT_LEVEL"] != "strict" {
		t.Fatalf("acme/web extensions %+v", s.Extensions)
	}
	var list []skills.ExtensionBody
	apptest.JSON(t, admin, http.MethodGet, e.Base+"/api/v1/extensions", nil, &list)
	if len(list) != 1 || list[0].Secrets["LINT_TOKEN"].Last4 != "0009" || list[0].Baked {
		t.Fatalf("extension list %+v", list)
	}
}
