package review_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/repos"
	"github.com/alternayte/sayso/internal/review"
)

func TestSCN_CFG_006_EffectiveConfigWithSources(t *testing.T) {
	e := apptest.New(t, apptest.Options{})
	e.GitHub.Install("acme/payments")
	e.SQL(t, `INSERT INTO mcp_servers (name, url) VALUES ('catalog', 'http://catalog.acme.test/mcp'), ('tickets', 'http://tickets.acme.test/mcp')`)
	e.SQL(t, `INSERT INTO profiles (name) VALUES ('review-strong')`)
	e.SQL(t, `UPDATE settings SET value = '["review","challenge","reply","fix","review-strong"]' WHERE key = 'allowed_profiles'`)
	e.SQL(t, `UPDATE repositories SET mcp_servers = '{catalog,tickets}', mode = 'comment' WHERE full_name = 'acme/payments'`)
	e.GitHub.CommitMain("acme/payments", map[string]string{repos.OverlayPath: "mcp: [catalog]\nmode: gate\nprofiles: {review: review-strong}\n"})

	c, _ := e.SignIn(t, "dev-alice")
	var b review.EffectiveBody
	if code := apptest.JSON(t, c, http.MethodGet, e.Base+"/api/v1/repositories/acme/payments/effective", nil, &b); code != http.StatusOK {
		t.Fatalf("effective: %d", code)
	}
	eff := b.Effective
	if !slices.Equal(eff.MCP, []string{"catalog"}) || eff.Sources["mcp"] != repos.SourceFile {
		t.Fatalf("mcp %v from %s", eff.MCP, eff.Sources["mcp"])
	}
	if eff.Profiles["review"] != "review-strong" || eff.Sources["profiles.review"] != repos.SourceFile {
		t.Fatalf("profiles %v (%v)", eff.Profiles, eff.Sources)
	}
	if eff.Profiles["challenge"] != "challenge" || eff.Sources["profiles.challenge"] != repos.SourceUI {
		t.Fatalf("challenge profile %s from %s", eff.Profiles["challenge"], eff.Sources["profiles.challenge"])
	}
	if b.Mode != "comment" {
		t.Fatalf("mode %s; the file must not set it", b.Mode)
	}
	if !slices.ContainsFunc(eff.Warnings, func(w string) bool { return strings.Contains(w, `"mode"`) }) {
		t.Fatalf("no mode warning: %v", eff.Warnings)
	}
	// The page reads each list, so none is JSON null.
	if eff.Extensions == nil || eff.Feeds == nil || eff.Setup == nil {
		t.Fatalf("null lists: %+v", eff)
	}
	if b.SkillSource != "builtin" {
		t.Fatalf("skill source %s", b.SkillSource)
	}
}
