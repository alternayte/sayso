// Package skills resolves the review skill of a round and reports run
// events and outcomes to Skael (§8.4, §14).
package skills

import (
	"context"
	"errors"
	"path"
	"slices"

	"github.com/alternayte/sayso/internal/runner"
	"github.com/alternayte/sayso/internal/secrets"
	"github.com/alternayte/sayso/internal/settings"
	builtinskill "github.com/alternayte/sayso/skills"
)

// Skael secrets. The agent key reaches agent runs; the reporter key never
// leaves the API (§14.1, SI-01).
const (
	AgentKeySecret    = "skael.agent_key"
	ReporterKeySecret = "skael.reporter_key"
)

// FileReader reads a file of a repository at a commit. It returns found
// false when the file does not exist.
type FileReader func(ctx context.Context, path, ref string) (data []byte, found bool, err error)

// Resolver picks the review skill of a round (§8.4).
type Resolver struct {
	Settings *settings.Store
	Secrets  *secrets.Store
}

// Resolve returns the skill of a round: the repository's skill on the base
// commit, else the Skael set, else the built-in skill. A Skael skill
// carries the built-in files too, so the run can fall back to them when the
// pull fails (REQ-SKL-003).
func (r *Resolver) Resolve(ctx context.Context, read FileReader, skillPath, baseSHA, repoSet string) (runner.Skill, *runner.Skael, error) {
	if _, found, err := read(ctx, skillPath, baseSHA); err != nil {
		return runner.Skill{}, nil, err
	} else if found {
		return runner.Skill{Source: "repository", Name: nameOf(skillPath), Path: skillPath, Commit: baseSHA}, nil, nil
	}
	builtin, err := builtinskill.Builtin()
	if err != nil {
		return runner.Skill{}, nil, err
	}
	fallback := runner.Skill{Source: "builtin", Name: builtinskill.BuiltinName, Files: builtin}
	sk, err := r.skael(ctx, repoSet)
	if err != nil || sk == nil {
		return fallback, nil, err
	}
	fallback.Source, fallback.Name = "skael", sk.SkillName
	return fallback, sk, nil
}

// skael returns the Skael set of a repository, or nil when Skael is not
// configured or no set applies.
func (r *Resolver) skael(ctx context.Context, repoSet string) (*runner.Skael, error) {
	var url, def, skill string
	var hook bool
	var allowed []string
	for _, kv := range []struct {
		key string
		out any
	}{{settings.SkaelURL, &url}, {settings.SkaelDefaultSet, &def}, {settings.SkaelReviewSkill, &skill}, {settings.SkaelActivationHook, &hook}, {settings.SkaelAllowedSets, &allowed}} {
		if err := r.Settings.Decode(ctx, kv.key, kv.out); err != nil {
			return nil, err
		}
	}
	set := def
	if repoSet != "" && (repoSet == def || slices.Contains(allowed, repoSet)) {
		set = repoSet
	}
	if url == "" || set == "" {
		return nil, nil
	}
	if _, err := r.Secrets.Meta(ctx, AgentKeySecret); err != nil {
		return nil, errors.New("skael: the agent key is not set")
	}
	return &runner.Skael{URL: url, Set: set, SkillName: skill, Hook: hook}, nil
}

// AgentKey returns the Skael agent key for the run spec.
func (r *Resolver) AgentKey(ctx context.Context) (string, error) {
	return r.Secrets.Value(ctx, AgentKeySecret)
}

func nameOf(p string) string { return path.Base(path.Dir(p)) }
