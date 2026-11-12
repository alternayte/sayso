package app

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Role is a process role of the sayso binary.
type Role string

const (
	RoleAll    Role = "all"
	RoleAPI    Role = "api"
	RoleWorker Role = "worker"
	RoleRelay  Role = "relay"
)

// ParseRole accepts the four role names.
func ParseRole(s string) (Role, error) {
	switch r := Role(s); r {
	case RoleAll, RoleAPI, RoleWorker, RoleRelay:
		return r, nil
	}
	return "", fmt.Errorf("unknown role %q: use all, api, worker or relay", s)
}

// RunsAPI reports whether the role serves the API.
func (r Role) RunsAPI() bool { return r == RoleAll || r == RoleAPI }

// RunsWorker reports whether the role runs Temporal workers.
func (r Role) RunsWorker() bool { return r == RoleAll || r == RoleWorker }

// Config is the process configuration from the environment (Appendix A).
// All other configuration lives in the database.
type Config struct {
	DatabaseURL       string
	SecretKey         []byte
	SecretKeyNew      []byte
	BaseURL           string
	PublicWebhookURL  string
	Ingress           string
	RelayURL          string
	RelayToken        string
	Role              Role
	Listen            string
	SetupToken        string
	BootstrapAdmins   []string
	TemporalAddress   string
	TemporalNamespace string
	TemporalTLSCert   string
	TemporalTLSKey    string
	TemporalAPIKey    string
	TemporalUIURL     string
	Runner            string
	RunnerAPIURL      string
	K8sNamespace      string
	ExtraCAPEM        string
	ExtraCAFile       string
	LogLevel          slog.Level
}

var knownVars = map[string]bool{
	"SAYSO_DATABASE_URL": true, "SAYSO_SECRET_KEY": true, "SAYSO_SECRET_KEY_NEW": true,
	"SAYSO_BASE_URL": true, "SAYSO_PUBLIC_WEBHOOK_URL": true, "SAYSO_INGRESS": true,
	"SAYSO_RELAY_URL": true, "SAYSO_RELAY_TOKEN": true, "SAYSO_ROLE": true, "SAYSO_LISTEN": true,
	"SAYSO_SETUP_TOKEN": true, "SAYSO_BOOTSTRAP_ADMINS": true,
	"SAYSO_TEMPORAL_ADDRESS": true, "SAYSO_TEMPORAL_NAMESPACE": true, "SAYSO_TEMPORAL_TLS_CERT": true,
	"SAYSO_TEMPORAL_TLS_KEY": true, "SAYSO_TEMPORAL_API_KEY": true, "SAYSO_TEMPORAL_UI_URL": true,
	"SAYSO_RUNNER": true, "SAYSO_RUNNER_API_URL": true, "SAYSO_K8S_NAMESPACE": true,
	"SAYSO_EXTRA_CA_PEM": true, "SAYSO_EXTRA_CA_FILE": true, "SAYSO_LOG_LEVEL": true,
}

// Need says which required variables a command needs.
type Need struct {
	Database  bool
	SecretKey bool
}

// LoadConfig reads the environment. It fails with a clear message when a
// required variable is missing, and returns the unknown SAYSO_* names so
// the caller can log a warning for each.
func LoadConfig(getenv func(string) string, environ []string, need Need) (*Config, []string, error) {
	c := &Config{
		DatabaseURL:       getenv("SAYSO_DATABASE_URL"),
		BaseURL:           strings.TrimRight(getenv("SAYSO_BASE_URL"), "/"),
		PublicWebhookURL:  getenv("SAYSO_PUBLIC_WEBHOOK_URL"),
		Ingress:           or(getenv("SAYSO_INGRESS"), "direct"),
		RelayURL:          getenv("SAYSO_RELAY_URL"),
		RelayToken:        getenv("SAYSO_RELAY_TOKEN"),
		Listen:            or(getenv("SAYSO_LISTEN"), ":8080"),
		SetupToken:        getenv("SAYSO_SETUP_TOKEN"),
		TemporalAddress:   or(getenv("SAYSO_TEMPORAL_ADDRESS"), "localhost:7233"),
		TemporalNamespace: or(getenv("SAYSO_TEMPORAL_NAMESPACE"), "default"),
		TemporalTLSCert:   getenv("SAYSO_TEMPORAL_TLS_CERT"),
		TemporalTLSKey:    getenv("SAYSO_TEMPORAL_TLS_KEY"),
		TemporalAPIKey:    getenv("SAYSO_TEMPORAL_API_KEY"),
		TemporalUIURL:     strings.TrimRight(getenv("SAYSO_TEMPORAL_UI_URL"), "/"),
		Runner:            or(getenv("SAYSO_RUNNER"), "process"),
		K8sNamespace:      getenv("SAYSO_K8S_NAMESPACE"),
		ExtraCAPEM:        getenv("SAYSO_EXTRA_CA_PEM"),
		ExtraCAFile:       getenv("SAYSO_EXTRA_CA_FILE"),
	}
	c.RunnerAPIURL = or(strings.TrimRight(getenv("SAYSO_RUNNER_API_URL"), "/"), c.BaseURL)
	for _, l := range strings.Split(getenv("SAYSO_BOOTSTRAP_ADMINS"), ",") {
		if l = strings.TrimSpace(l); l != "" {
			c.BootstrapAdmins = append(c.BootstrapAdmins, l)
		}
	}
	role, err := ParseRole(or(getenv("SAYSO_ROLE"), "all"))
	if err != nil {
		return nil, nil, fmt.Errorf("SAYSO_ROLE: %w", err)
	}
	c.Role = role
	if c.Ingress != "direct" && c.Ingress != "relay" {
		return nil, nil, fmt.Errorf("SAYSO_INGRESS: %q is not direct or relay", c.Ingress)
	}
	if c.Runner != "process" && c.Runner != "kubernetes" {
		return nil, nil, fmt.Errorf("SAYSO_RUNNER: %q is not process or kubernetes", c.Runner)
	}
	if lvl := getenv("SAYSO_LOG_LEVEL"); lvl != "" {
		if err := c.LogLevel.UnmarshalText([]byte(lvl)); err != nil {
			return nil, nil, fmt.Errorf("SAYSO_LOG_LEVEL: %w", err)
		}
	}
	if need.Database && c.DatabaseURL == "" {
		return nil, nil, fmt.Errorf("SAYSO_DATABASE_URL is required: set it to the PostgreSQL URL")
	}
	if need.SecretKey {
		if c.SecretKey, err = decodeKey("SAYSO_SECRET_KEY", getenv("SAYSO_SECRET_KEY"), true); err != nil {
			return nil, nil, err
		}
	}
	if c.SecretKeyNew, err = decodeKey("SAYSO_SECRET_KEY_NEW", getenv("SAYSO_SECRET_KEY_NEW"), false); err != nil {
		return nil, nil, err
	}
	var unknown []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "SAYSO_") && !knownVars[name] && !strings.HasPrefix(name, "SAYSO_LIVE_") {
			unknown = append(unknown, name)
		}
	}
	return c, unknown, nil
}

func decodeKey(name, v string, required bool) ([]byte, error) {
	if v == "" {
		if required {
			return nil, fmt.Errorf("%s is required: set it to 32 random bytes in base64 (openssl rand -base64 32)", name)
		}
		return nil, nil
	}
	k, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(k) != 32 {
		return nil, fmt.Errorf("%s must be 32 bytes in base64 (openssl rand -base64 32)", name)
	}
	return k, nil
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// LoadFromProcess reads the configuration of this process.
func LoadFromProcess(need Need) (*Config, []string, error) {
	return LoadConfig(os.Getenv, os.Environ(), need)
}

func getenv(name string) string { return os.Getenv(name) }
