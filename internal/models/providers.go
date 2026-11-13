// Package models holds providers, profiles, the provider smoke test, the
// model proxy, usage and budgets (§12, §7.3).
package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// API types (C-07).
const (
	OpenAICompletions = "openai-completions"
	OpenAIResponses   = "openai-responses"
	AnthropicMessages = "anthropic-messages"
	GoogleGenerative  = "google-generative-ai"
)

// Authentication styles (§12.1).
const (
	AuthBearer  = "bearer"
	AuthXAPIKey = "x_api_key"
	AuthAPIKey  = "api_key"
	AuthHeader  = "header"
)

// Config is a provider as the proxy and the smoke test use it.
type Config struct {
	Name            string
	APIType         string
	BaseURL         string
	AuthStyle       string
	AuthHeader      string
	SecretID        *uuid.UUID
	Headers         map[string]string
	Compat          json.RawMessage
	Models          []string
	MaxConcurrency  int
	TokensPerMinute int
}

func configOf(p Provider) (Config, error) {
	c := Config{Name: p.Name, APIType: p.ApiType, BaseURL: p.BaseUrl, AuthStyle: p.AuthStyle, AuthHeader: p.AuthHeader,
		SecretID: p.SecretID, Compat: p.Compat, Models: p.Models, MaxConcurrency: int(p.MaxConcurrency), TokensPerMinute: int(p.TokensPerMinute)}
	if err := json.Unmarshal(p.Headers, &c.Headers); err != nil {
		return c, fmt.Errorf("provider %s headers: %w", p.Name, err)
	}
	return c, nil
}

// credentialHeaders are the request headers that can carry a model key.
// The proxy strips all of them before it adds the provider credential.
var credentialHeaders = []string{"Authorization", "X-Api-Key", "Api-Key", "X-Goog-Api-Key"}

// SetCredential adds the provider key in the provider's authentication
// style and the extra headers.
func (c Config) SetCredential(h http.Header, key string) {
	for k, v := range c.Headers {
		h.Set(k, v)
	}
	if key == "" {
		return
	}
	switch c.AuthStyle {
	case AuthBearer:
		h.Set("Authorization", "Bearer "+key)
	case AuthXAPIKey:
		h.Set("x-api-key", key)
	case AuthAPIKey:
		h.Set("api-key", key)
	case AuthHeader:
		h.Set(c.AuthHeader, key)
	}
}

// inference is the request path of each API type that the proxy forwards;
// any other path of the provider is refused (SI-03).
var inference = map[string]*regexp.Regexp{
	"openai-completions":   regexp.MustCompile(`^chat/completions$`),
	"openai-responses":     regexp.MustCompile(`^responses$`),
	"anthropic-messages":   regexp.MustCompile(`^messages$`),
	"google-generative-ai": regexp.MustCompile(`^models/[A-Za-z0-9._-]+:(generateContent|streamGenerateContent)$`),
}

// ErrPath means a request path that is not an inference call.
var ErrPath = errors.New("the path is not an inference call of the provider API")

// Endpoint joins the base URL and an inference path with a query. The
// path must be the inference call of the API type, with no dot segment and
// no encoded slash, so the key reaches no other endpoint of the host.
func (c Config) Endpoint(path, rawQuery string) (string, error) {
	p := strings.Trim(path, "/")
	if strings.Contains(p, "%") || strings.Contains(p, "\\") {
		return "", ErrPath
	}
	if re := inference[c.APIType]; re == nil || !re.MatchString(p) {
		return "", ErrPath
	}
	u, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/" + strings.TrimLeft(path, "/"))
	if err != nil {
		return "", err
	}
	u.RawQuery = rawQuery
	return u.String(), nil
}

var headerName = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
var providerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Validate checks the fields of a provider before save.
func (c Config) Validate() error {
	if !providerName.MatchString(c.Name) {
		return fmt.Errorf("name: use lower-case letters, digits and hyphens")
	}
	switch c.APIType {
	case OpenAICompletions, OpenAIResponses, AnthropicMessages, GoogleGenerative:
	default:
		return fmt.Errorf("api_type: %q is not supported", c.APIType)
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("base_url: give an http or https URL")
	}
	switch c.AuthStyle {
	case AuthBearer, AuthXAPIKey, AuthAPIKey:
	case AuthHeader:
		if !headerName.MatchString(c.AuthHeader) {
			return fmt.Errorf("auth_header: give a header name")
		}
	default:
		return fmt.Errorf("auth_style: %q is not supported", c.AuthStyle)
	}
	for k := range c.Headers {
		if !headerName.MatchString(k) {
			return fmt.Errorf("headers: %q is not a header name", k)
		}
		for _, cred := range credentialHeaders {
			if strings.EqualFold(k, cred) {
				return fmt.Errorf("headers: put the key in the key field, not in %s", k)
			}
		}
	}
	if c.MaxConcurrency < 0 || c.TokensPerMinute < 0 {
		return fmt.Errorf("limits must be 0 (no limit) or more")
	}
	return nil
}

// Preset fills the defaults of a known provider (§12.1). Presets are only
// defaults; every field stays editable.
type Preset struct {
	ID         string            `json:"id"`
	Label      string            `json:"label"`
	APIType    string            `json:"api_type"`
	BaseURL    string            `json:"base_url"`
	AuthStyle  string            `json:"auth_style"`
	AuthHeader string            `json:"auth_header,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Hint       string            `json:"hint,omitempty"`
}

// Presets of §12.1.
var Presets = []Preset{
	{ID: "openai", Label: "OpenAI", APIType: OpenAIResponses, BaseURL: "https://api.openai.com/v1", AuthStyle: AuthBearer},
	{ID: "anthropic", Label: "Anthropic", APIType: AnthropicMessages, BaseURL: "https://api.anthropic.com/v1", AuthStyle: AuthXAPIKey,
		Headers: map[string]string{"anthropic-version": "2023-06-01"}},
	{ID: "azure-foundry", Label: "Azure AI Foundry", APIType: OpenAICompletions, BaseURL: "https://RESOURCE.openai.azure.com/openai/v1", AuthStyle: AuthAPIKey,
		Hint: "Replace RESOURCE with the name of your Azure resource."},
	{ID: "openrouter", Label: "OpenRouter", APIType: OpenAICompletions, BaseURL: "https://openrouter.ai/api/v1", AuthStyle: AuthBearer},
	{ID: "deepseek", Label: "DeepSeek", APIType: OpenAICompletions, BaseURL: "https://api.deepseek.com/v1", AuthStyle: AuthBearer},
	{ID: "cloudflare-gateway", Label: "Cloudflare AI Gateway", APIType: OpenAICompletions,
		BaseURL: "https://gateway.ai.cloudflare.com/v1/ACCOUNT/GATEWAY/compat", AuthStyle: AuthBearer,
		Hint: "Replace ACCOUNT and GATEWAY with your account ID and gateway name."},
	{ID: "openai-compatible", Label: "OpenAI-compatible gateway", APIType: OpenAICompletions, BaseURL: "https://", AuthStyle: AuthBearer,
		Hint: "Give the base URL of the gateway, up to and including /v1."},
}

func secretName(provider string) string { return "provider." + provider + ".key" }

// Key returns the plaintext key of a provider, or "" when it has none.
func (s *Service) Key(ctx context.Context, c Config) (string, error) {
	return s.secrets.ValueOf(ctx, c.SecretID)
}
