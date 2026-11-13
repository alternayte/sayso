package app

import (
	"context"
	"os"

	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/guidance"
	"github.com/alternayte/sayso/internal/publish"
	"github.com/alternayte/sayso/internal/review"
	"github.com/alternayte/sayso/internal/runner"
)

// gitHubSession returns a client with an installation token, for
// activities that write to a repository.
func gitHubSession(d *Deps, f *Features) func(ctx context.Context, inst int64, fullName string) (publish.Session, error) {
	return func(ctx context.Context, inst int64, _ string) (publish.Session, error) {
		app, err := github.LoadApp(ctx, d.Pool, f.Secrets)
		if err != nil {
			return publish.Session{}, err
		}
		gh, err := f.GitHubClient(ctx)
		if err != nil {
			return publish.Session{}, err
		}
		tok, err := f.Tokens.Get(ctx, gh, app, inst)
		if err != nil {
			return publish.Session{}, err
		}
		return publish.Session{Client: gh, Token: tok, Inst: inst}, nil
	}
}

// ReviewActivities builds the activities of the review workflows.
func ReviewActivities(d *Deps, f *Features) *review.Activities {
	gh := gitHubSession(d, f)
	return &review.Activities{
		Pool: d.Pool, Settings: f.Settings, GitHub: gh,
		Publisher:  &publish.Publisher{Pool: d.Pool, Settings: f.Settings, GitHub: gh, BaseURL: d.Config.BaseURL, AppLogin: appLogin(d, f)},
		Skills:     f.Skills,
		Extensions: f.Extensions,
		Feeds:      f.Feeds,
		Reporter:   f.Reporter,
		Stores:     f.Stores,
		Guidance:   &guidance.Store{Pool: d.Pool},
		AppLogin:   appLogin(d, f),
	}
}

// AgentRunner builds the configured runner (§8.2).
func AgentRunner(cfg *Config) (runner.Runner, error) {
	if cfg.Runner == "kubernetes" {
		return KubernetesRunner(cfg)
	}
	return &runner.Process{Getenv: os.Getenv}, nil
}

// AgentActivities builds the agent run activities.
func AgentActivities(d *Deps, f *Features) (*review.AgentActivities, error) {
	r := d.Runner
	if r == nil {
		var err error
		if r, err = AgentRunner(d.Config); err != nil {
			return nil, err
		}
	}
	return &review.AgentActivities{Pool: d.Pool, Extensions: f.Extensions, Feeds: f.Feeds, Runner: r, RunnerKind: d.Config.Runner, APIURL: d.Config.RunnerAPIURL}, nil
}

// appLogin is the login of the App bot, for REQ-LIFE-011 and the marker check.
func appLogin(d *Deps, f *Features) func(ctx context.Context) string {
	return func(ctx context.Context) string {
		app, err := github.LoadApp(ctx, d.Pool, f.Secrets)
		if err != nil {
			return ""
		}
		return app.Slug + "[bot]"
	}
}
