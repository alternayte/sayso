package models_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/sayso/internal/app/apptest"
	"github.com/alternayte/sayso/internal/models"
	"github.com/alternayte/sayso/internal/secrets"
)

func TestSCN_MOD_001_PresetsAndProviderLifecycle(t *testing.T) {
	e := newEnv(t)
	var presets []models.Preset
	if code := apptest.JSON(t, e.admin, http.MethodGet, e.Base+"/api/v1/providers/presets", nil, &presets); code != 200 {
		t.Fatalf("presets: %d", code)
	}
	want := map[string]string{"OpenAI": "", "Anthropic": "", "Azure AI Foundry": "", "OpenRouter": "", "DeepSeek": "", "Cloudflare AI Gateway": "", "OpenAI-compatible gateway": ""}
	for _, p := range presets {
		delete(want, p.Label)
		if p.APIType == "" || p.BaseURL == "" || p.AuthStyle == "" {
			t.Fatalf("preset %s lacks a default: %+v", p.Label, p)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing presets: %v", want)
	}
	anth := presets[slices.IndexFunc(presets, func(p models.Preset) bool { return p.ID == "anthropic" })]
	if anth.APIType != models.AnthropicMessages || anth.AuthStyle != models.AuthXAPIKey || anth.Headers["anthropic-version"] == "" {
		t.Fatalf("anthropic preset %+v", anth)
	}

	// Save a provider from a preset, edit it and delete it.
	in := models.ProviderInput{Name: "anthropic-main", APIType: anth.APIType, BaseURL: anth.BaseURL, AuthStyle: anth.AuthStyle, Headers: anth.Headers,
		Key: key("sk-ant-0001"), Models: []string{"claude-x"}}
	created := e.provider(t, in)
	if created.BaseURL != anth.BaseURL || !created.Key.Set || created.Key.Last4 != "0001" {
		t.Fatalf("created %+v", created)
	}
	in.BaseURL = "https://gateway.acme.test/anthropic/v1"
	in.Key = nil
	var updated models.ProviderBody
	if code := apptest.JSON(t, e.admin, http.MethodPut, e.Base+"/api/v1/providers/anthropic-main", in, &updated); code != 200 {
		t.Fatalf("update: %d", code)
	}
	if updated.BaseURL != in.BaseURL || updated.Key.Last4 != "0001" {
		t.Fatalf("update changed the key or lost the URL: %+v", updated)
	}
	if code := apptest.JSON(t, e.admin, http.MethodDelete, e.Base+"/api/v1/providers/anthropic-main", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	var list []models.ProviderBody
	apptest.JSON(t, e.admin, http.MethodGet, e.Base+"/api/v1/providers", nil, &list)
	if len(list) != 0 || e.Count(t, `SELECT count(*) FROM secrets WHERE name = 'provider.anthropic-main.key'`) != 0 {
		t.Fatalf("delete left %d providers or the secret", len(list))
	}
	if n := e.Count(t, `SELECT count(*) FROM audit_log WHERE action IN ('provider.create', 'provider.update', 'provider.delete')`); n != 3 {
		t.Fatalf("audit rows: %d", n)
	}
	// Viewers and maintainers cannot read providers.
	viewer, _ := e.SignIn(t, "dev-bob")
	if code := apptest.JSON(t, viewer, http.MethodGet, e.Base+"/api/v1/providers", nil, nil); code != http.StatusForbidden {
		t.Fatalf("viewer: %d", code)
	}
}

func TestSCN_MOD_003_DefaultProfilesAndAllowedChoice(t *testing.T) {
	e := newEnv(t)
	var profiles []models.ProfileBody
	apptest.JSON(t, e.admin, http.MethodGet, e.Base+"/api/v1/profiles", nil, &profiles)
	var names []string
	for _, p := range profiles {
		names = append(names, p.Name)
		if !p.Default {
			t.Fatalf("profile %s is not a default", p.Name)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"challenge", "fix", "reply", "review"}) {
		t.Fatalf("profiles %v", names)
	}
	if code := apptest.JSON(t, e.admin, http.MethodDelete, e.Base+"/api/v1/profiles/review", nil, nil); code != http.StatusConflict {
		t.Fatalf("delete default: %d", code)
	}
	// A new profile that is not in the allowed list cannot be chosen.
	if code := apptest.JSON(t, e.admin, http.MethodPut, e.Base+"/api/v1/profiles/review-strong", map[string]any{"targets": []any{}}, nil); code != 200 {
		t.Fatalf("new profile: %d", code)
	}
	e.GitHub.Install("acme/payments")
	choice := map[string]string{"review": "review-strong", "challenge": "challenge", "reply": "reply", "fix": "fix"}
	if code := apptest.JSON(t, e.admin, http.MethodPut, e.Base+"/api/v1/repositories/acme/payments/profiles", choice, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("not allowed profile: %d", code)
	}
	if code := apptest.JSON(t, e.admin, http.MethodPut, e.Base+"/api/v1/settings/allowed_profiles", map[string]any{"value": []string{"review", "review-strong", "challenge", "reply", "fix"}}, nil); code != 200 {
		t.Fatalf("allow profile: %d", code)
	}
	var repo struct {
		Profiles map[string]string `json:"profiles"`
	}
	if code := apptest.JSON(t, e.admin, http.MethodPut, e.Base+"/api/v1/repositories/acme/payments/profiles", choice, &repo); code != 200 || repo.Profiles["review"] != "review-strong" {
		t.Fatalf("allowed profile: %d %+v", code, repo)
	}
}

func TestSCN_CFG_002_ProviderKeyNeverReturned(t *testing.T) {
	e := newEnv(t)
	const plain = "sk-acme-provider-secret-7731"
	e.provider(t, models.ProviderInput{Name: "main", APIType: models.OpenAICompletions, AuthStyle: models.AuthBearer, Key: key(plain), Models: []string{"fake-large"}})
	e.provider(t, models.ProviderInput{Name: "from-env", APIType: models.OpenAICompletions, AuthStyle: models.AuthBearer,
		Key: &secrets.Input{Env: "ACME_TEST_UNSET_PROVIDER_KEY"}, Models: []string{"fake-large"}})

	if n := e.Count(t, `SELECT count(*) FROM secrets WHERE name = 'provider.main.key' AND position(convert_to($1, 'UTF8') in ciphertext) = 0`, plain); n != 1 {
		t.Fatal("the stored value is not encrypted")
	}
	// No API response contains the key.
	for _, path := range []string{"/api/v1/providers", "/api/v1/providers/main/models", "/api/v1/audit", "/api/v1/settings"} {
		req, _ := http.NewRequest(http.MethodGet, e.Base+path, nil)
		resp, err := e.admin.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if bytes.Contains(b, []byte(plain)) {
			t.Fatalf("%s returns the key", path)
		}
	}
	var list []models.ProviderBody
	apptest.JSON(t, e.admin, http.MethodGet, e.Base+"/api/v1/providers", nil, &list)
	byName := map[string]models.ProviderBody{}
	for _, p := range list {
		byName[p.Name] = p
	}
	if k := byName["main"].Key; !k.Set || k.Last4 != "7731" || k.Status != "stored" || k.UpdatedAt == nil {
		t.Fatalf("main key field %+v", k)
	}
	if k := byName["from-env"].Key; k.Kind != "env_ref" || k.Status != "not set" || k.EnvName != "ACME_TEST_UNSET_PROVIDER_KEY" {
		t.Fatalf("env key field %+v", k)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), plain) {
		t.Fatal("the provider list holds the key")
	}
}
