package app_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/auth"
)

// required is the minimum role of each operation, from §17 and Appendix D.
// An operation that is missing here fails the test, so each new route
// states its rights.
var required = map[string]string{
	"get-me":                  "viewer",
	"get-setup-state":         "public",
	"create-setup-session":    "public",
	"get-access":              "admin",
	"put-team-role":           "admin",
	"delete-team-role":        "admin",
	"put-grant":               "admin",
	"delete-grant":            "admin",
	"list-settings":           "admin",
	"put-setting":             "admin",
	"get-setting-history":     "admin",
	"list-audit":              "admin",
	"list-audit-actions":      "admin",
	"get-diagnostics":         "admin",
	"create-app-manifest":     "setup",
	"list-repositories":       "viewer",
	"get-repository":          "viewer",
	"set-repository-mode":     "maintainer",
	"get-github":              "admin",
	"set-repository-profiles": "maintainer",
	"list-provider-presets":   "admin",
	"list-providers":          "admin",
	"create-provider":         "admin",
	"update-provider":         "admin",
	"delete-provider":         "admin",
	"test-provider":           "admin",
	"list-provider-models":    "admin",
	"list-profiles":           "admin",
	"put-profile":             "admin",
	"delete-profile":          "admin",
	"list-mcp-servers":        "admin",
	"create-mcp-server":       "admin",
	"update-mcp-server":       "admin",
	"delete-mcp-server":       "admin",
	"test-mcp-server":         "admin",
	"set-repository-mcp":      "maintainer",
	"set-repository-feeds":    "maintainer",
	"get-status":              "viewer",
	"list-reviews":            "viewer",
	"get-review":              "viewer",
	"get-round":               "viewer",
	"cancel-round":            "maintainer",
	"set-repository-review":   "maintainer",
	"list-feeds":              "admin",
	"put-feed":                "admin",
	"delete-feed":             "admin",
	"test-feed":               "admin",
	"list-extensions":         "admin",
	"put-extension":           "admin",
	"delete-extension":        "admin",
	"get-skills":              "admin",
	"put-skael":               "admin",
	"list-agent-images":       "admin",
	"put-agent-image":         "admin",
	"delete-agent-image":      "admin",
}

var rank = map[string]int{"anonymous": 0, "viewer": 1, "maintainer": 2, "admin": 3}

func TestSCN_AUTH_005_RouteRights(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	ctx := t.Context()
	if err := e.F.Auth.Grant(ctx, audit.System, "dev-alice", auth.Maintainer); err != nil {
		t.Fatal(err)
	}
	if err := e.F.Auth.Grant(ctx, audit.System, "octo-admin", auth.Admin); err != nil {
		t.Fatal(err)
	}
	clients := map[string]*http.Client{"anonymous": e.Client(t)}
	clients["viewer"], _ = e.SignIn(t, "dev-bob")
	clients["maintainer"], _ = e.SignIn(t, "dev-alice")
	clients["admin"], _ = e.SignIn(t, "octo-admin")

	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if code := apptest.JSON(t, clients["anonymous"], http.MethodGet, e.Base+"/api/v1/openapi.json", nil, &doc); code != 200 {
		t.Fatalf("openapi: %d", code)
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	seen := map[string]bool{}
	for path, ops := range doc.Paths {
		for method, op := range ops {
			min, ok := required[op.OperationID]
			if !ok {
				t.Errorf("operation %s (%s %s) has no expected role", op.OperationID, method, path)
				continue
			}
			seen[op.OperationID] = true
			url := e.Base + param.ReplaceAllString(path, "x")
			for who, c := range clients {
				var body any
				if m := strings.ToUpper(method); m == http.MethodPut || m == http.MethodPost {
					body = json.RawMessage(`{}`)
				}
				code := apptest.JSON(t, c, strings.ToUpper(method), url, body, nil)
				if min == "setup" {
					// Setup operations end when the App exists (SI-14).
					if code != http.StatusNotFound {
						t.Errorf("%s %s as %s: %d, want 404 after setup", method, path, who, code)
					}
					continue
				}
				denied := code == http.StatusUnauthorized || code == http.StatusForbidden
				allowed := min == "public" || rank[who] >= rank[min]
				if allowed && denied && op.OperationID != "create-setup-session" {
					t.Errorf("%s %s as %s: %d, want access", method, path, who, code)
				}
				if !allowed && !denied {
					t.Errorf("%s %s as %s: %d, want 401 or 403", method, path, who, code)
				}
				if !allowed && who == "anonymous" && code != http.StatusUnauthorized {
					t.Errorf("%s %s anonymous: %d, want 401", method, path, code)
				}
			}
		}
	}
	for id := range required {
		if !seen[id] {
			t.Errorf("operation %s is in the table but not in the API", id)
		}
	}
}
