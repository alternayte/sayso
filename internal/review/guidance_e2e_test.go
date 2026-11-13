//go:build e2e

package review_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/audit"
	"github.com/alternayte/sayso/internal/auth"
	"github.com/alternayte/sayso/internal/guidance"
)

func TestSCN_CFG_011_GuidanceVersionsPreviewAndPromptRef(t *testing.T) {
	e := real(t, "comment")
	// Sign-in redirects the browser to the GitHub web URL, which the agent
	// container reaches by another host name.
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
	admin, _ := e.SignIn(t, "octo-admin")
	web(e.ContainerURL(containerHost, e.GHServer))
	put := func(path, text string) guidance.GuidanceBody {
		t.Helper()
		var out guidance.GuidanceBody
		if code := apptest.JSON(t, admin, http.MethodPut, e.Base+"/api/v1"+path, map[string]string{"text": text}, &out); code != http.StatusOK {
			t.Fatalf("PUT %s: %d", path, code)
		}
		return out
	}
	put("/guidance", "Prefer small, focused findings.")
	put("/repositories/acme/payments/guidance", "Money is in integer cents.")
	h := put("/guidance", "Prefer small, focused findings. Check error paths first.")
	if len(h.History) != 2 || h.Current == nil || h.Current.Version != 2 || h.History[1].Version != 1 {
		t.Fatalf("global history: %+v", h)
	}

	pr := e.script(t, "e2e-caller", map[string]string{"ledger/ledger.go": noCheck})

	var pv guidance.PreviewBody
	if code := apptest.JSON(t, admin, http.MethodGet, e.Base+"/api/v1/repositories/acme/payments/prompt-preview?stage=review", nil, &pv); code != http.StatusOK {
		t.Fatalf("preview: %d", code)
	}
	var names []string
	for _, l := range pv.Prompt.Layers {
		names = append(names, l.Name)
	}
	if want := []string{"contract", "global", "repository", "file", "skill"}; !slices.Equal(names, want) {
		t.Fatalf("layers: %v", names)
	}
	if pv.Versions.Global != "2" || pv.Versions.Repository != "1" {
		t.Fatalf("preview versions: %+v", pv.Versions)
	}

	waitFor(t, 4*time.Minute, "round", func() bool { return e.rounds(t, "acme/payments", pr.Number, "done") == 1 })
	var raw []byte
	if err := e.Deps.Pool.QueryRow(t.Context(), `SELECT ro.prompt_ref FROM rounds ro JOIN reviews rv ON rv.id = ro.review_id WHERE rv.pr_number = $1`, pr.Number).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Global     string            `json:"global"`
		Repository string            `json:"repository"`
		Stages     map[string]string `json:"stages"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.Global != "2" || ref.Repository != "1" {
		t.Fatalf("prompt_ref versions: %s", raw)
	}
	if ref.Stages["review"] != pv.Prompt.SHA256 {
		t.Fatalf("prompt_ref review hash %s, preview %s", ref.Stages["review"], pv.Prompt.SHA256)
	}
}
