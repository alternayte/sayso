//go:build e2e

package review_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/auth"
	fakegh "github.com/alternayte/sayso/internal/fakes/github"
	ghapi "github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/review"
)

// admin signs in octo-admin as an admin. Sign-in redirects the browser to
// the GitHub web URL, which the agent container reaches by another host name.
func (e *env) admin(t *testing.T) *http.Client {
	t.Helper()
	web := func(u string) {
		raw, _ := json.Marshal(u)
		if _, err := e.F.Settings.Set(t.Context(), audit.System, "github_web_url", raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.F.Auth.Grant(t.Context(), audit.System, "octo-admin", auth.Admin); err != nil {
		t.Fatal(err)
	}
	web(e.GHServer.URL)
	c, _ := e.SignIn(t, "octo-admin")
	web(e.ContainerURL(containerHost, e.GHServer))
	return c
}

func TestSCN_CFG_008_RunnerTestAndRedelivery(t *testing.T) {
	e := real(t, "off")
	admin := e.admin(t)

	var res review.RunnerTestResult
	if code := apptest.JSON(t, admin, http.MethodPost, e.Base+"/api/v1/runner/test", nil, &res); code != http.StatusOK {
		t.Fatalf("runner test: %d", code)
	}
	if res.Status != "done" || !strings.HasPrefix(res.HarnessVersion, "pi") {
		t.Fatalf("runner test %+v", res)
	}
	if n := e.Count(t, `SELECT count(*) FROM agent_runs WHERE id = $1 AND kind = 'noop'`, res.RunID); n != 1 {
		t.Fatalf("noop runs: %d", n)
	}

	var list []ghapi.RemoteDeliveryBody
	if code := apptest.JSON(t, admin, http.MethodGet, e.Base+"/api/v1/github/deliveries", nil, &list); code != http.StatusOK || len(list) == 0 {
		t.Fatalf("deliveries: %d %v", code, list)
	}
	d := list[0]
	if d.GUID == "" || d.Event == "" || !d.Handled {
		t.Fatalf("delivery %+v", d)
	}
	before := redeliveries(e.GitHub.Deliveries(), d.GUID)
	url := fmt.Sprintf("%s/api/v1/github/deliveries/%d/redeliver", e.Base, d.ID)
	if code := apptest.JSON(t, admin, http.MethodPost, url, map[string]string{"guid": d.GUID}, nil); code != http.StatusAccepted {
		t.Fatalf("redeliver: %d", code)
	}
	waitFor(t, 30*time.Second, "redelivery", func() bool { return redeliveries(e.GitHub.Deliveries(), d.GUID) == before+1 })
	if n := e.Count(t, `SELECT count(*) FROM audit_log WHERE action = 'github.redelivery' AND target = $1 AND actor_login = 'octo-admin'`, d.GUID); n != 1 {
		t.Fatalf("audit rows: %d", n)
	}
}

func redeliveries(all []fakegh.Delivery, guid string) int {
	n := 0
	for _, d := range all {
		if d.GUID == guid && d.Redelivery {
			n++
		}
	}
	return n
}
