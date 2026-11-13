package main

import (
	"log/slog"
	"os"

	"github.com/alternayte/sayso/internal/app"
)

// setup loads the configuration and builds the logger. It logs a warning
// for each unknown SAYSO_* variable.
func setup(need app.Need) (*app.Config, *slog.Logger, *app.Redactor, error) {
	cfg, unknown, err := app.LoadFromProcess(need)
	if err != nil {
		return nil, nil, nil, err
	}
	red := app.NewRedactor()
	red.Add("database_url", cfg.DatabaseURL)
	red.Add("setup_token", cfg.SetupToken)
	red.Add("relay_token", cfg.RelayToken)
	red.Add("temporal_api_key", cfg.TemporalAPIKey)
	// The key as the variable holds it; stored text never shows it (SI-05).
	red.Add("secret_key", os.Getenv("SAYSO_SECRET_KEY"))
	red.Add("secret_key_new", os.Getenv("SAYSO_SECRET_KEY_NEW"))
	log := app.NewLogger(os.Stderr, cfg.LogLevel, red)
	for _, name := range unknown {
		log.Warn("unknown variable", "name", name)
	}
	return cfg, log, red, nil
}
