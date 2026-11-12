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
	log := app.NewLogger(os.Stderr, cfg.LogLevel, red)
	for _, name := range unknown {
		log.Warn("unknown variable", "name", name)
	}
	return cfg, log, red, nil
}
