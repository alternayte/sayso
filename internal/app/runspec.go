package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/alternayte/sayso/internal/github"
	"github.com/alternayte/sayso/internal/repos"
	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/settings"
	"github.com/alternayte/sayso/internal/skills"
)

// enrichSpec adds what the stored spec must not hold: a read-only token for
// the one repository, minted now (§6.1), and the proxy URLs of the models and
// MCP servers (§8.5). The runtime uses its run token as the key of each.
func enrichSpec(d *Deps, f *Features) func(context.Context, runner.AgentRun, *runner.Spec) error {
	return func(ctx context.Context, run runner.AgentRun, spec *runner.Spec) error {
		base := strings.TrimRight(d.Config.RunnerAPIURL, "/")
		ms, err := runner.AllowedModels(run)
		if err != nil {
			return err
		}
		spec.Models = spec.Models[:0]
		for _, m := range ms {
			api, compat, err := f.Models.APIOf(ctx, m.Provider)
			if err != nil {
				return err
			}
			spec.Models = append(spec.Models, runner.SpecModel{Profile: m.Profile, Provider: m.Provider, Model: m.Model, API: api,
				BaseURL: base + "/llm/" + m.Provider, Compat: compat, ContextWindow: m.ContextWindow, MaxTokens: m.MaxTokens,
				Reasoning: m.Reasoning, Temperature: m.Temperature})
		}
		for i := range spec.MCP {
			spec.MCP[i].URL = base + "/mcp/" + spec.MCP[i].Name
		}
		// The agent key is the only Skael credential in a run (§14.1).
		if spec.Skael != nil {
			key, err := f.Secrets.Value(ctx, skills.AgentKeySecret)
			if err != nil {
				return err
			}
			spec.Skael.AgentKey = key
			if run.RoundID != nil {
				spec.Skael.ExternalRunID = run.RoundID.String()
			}
		}
		if spec.Repository.FullName == "" || run.RepositoryID == nil {
			return nil
		}
		repo, err := repos.New(d.Pool).ByFullName(ctx, spec.Repository.FullName)
		if err != nil {
			return fmt.Errorf("repository %s: %w", spec.Repository.FullName, err)
		}
		if repo.ID != *run.RepositoryID {
			return fmt.Errorf("the spec repository is not the run repository")
		}
		app, err := github.LoadApp(ctx, d.Pool, f.Secrets)
		if err != nil {
			return err
		}
		gh, err := f.GitHubClient(ctx)
		if err != nil {
			return err
		}
		name := spec.Repository.FullName[strings.IndexByte(spec.Repository.FullName, '/')+1:]
		tok, err := gh.InstallationToken(ctx, app, repo.InstallationID, github.TokenRequest{Repositories: []string{name}, Permissions: github.ReadOnlyRun})
		if err != nil {
			return err
		}
		spec.Repository.Token = tok
		if spec.Repository.CloneURL == "" {
			web, err := f.Settings.String(ctx, settings.GitHubWebURL)
			if err != nil {
				return err
			}
			spec.Repository.CloneURL = strings.TrimRight(web, "/") + "/" + spec.Repository.FullName + ".git"
		}
		return nil
	}
}

// feedGitHubToken mints an installation token with contents:read for the
// repositories of a github feed (§8.11).
func feedGitHubToken(ctx context.Context, d *Deps, f *Features, full []string) (string, string, error) {
	app, err := github.LoadApp(ctx, d.Pool, f.Secrets)
	if err != nil {
		return "", "", err
	}
	gh, err := f.GitHubClient(ctx)
	if err != nil {
		return "", "", err
	}
	var inst int64
	var names []string
	for _, r := range full {
		repo, err := repos.New(d.Pool).ByFullName(ctx, r)
		if err != nil {
			return "", "", fmt.Errorf("feed repository %s: %w", r, err)
		}
		inst = repo.InstallationID
		names = append(names, r[strings.IndexByte(r, '/')+1:])
	}
	tok, err := gh.InstallationToken(ctx, app, inst, github.TokenRequest{Repositories: names, Permissions: map[string]string{"contents": "read", "metadata": "read"}})
	return tok, gh.WebURL, err
}
