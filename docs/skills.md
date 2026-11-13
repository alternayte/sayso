# Skills, guidance and `.sayso.yml`

The agent follows a review skill. Admins and repositories can add guidance and pi extensions. A repository can tune its reviews with `.sayso.yml` on its base branch.

Sayso reads the skill, the overlay, `AGENTS.md` and `CLAUDE.md` from the base commit only. A pull request cannot change the rules of its own review.

## Skill resolution

Each round uses one review skill. Sayso picks the first source that applies:

1. **Repository.** The skill file at the path in `.sayso.yml` `skill`, on the base commit. The default path is `.agents/skills/pr-review/SKILL.md`. The run copies the whole skill folder.
2. **Skael.** When Skael is configured and a set applies to the repository, the run pulls the set from Skael. It uses the skill named in the `skael_review_skill` setting (default `pr-review`).
3. **Built-in.** The `pr-review` skill that is part of the binary. Settings → Skills shows it.

When the Skael pull fails, the run uses the built-in skill and records an `error` event.

Each round records the skill source, the name, the version or commit, and the SHA-256 of the skill folder. The summary comment footer shows the skill source and version.

### Write a repository skill

Put the skill in the default path on the default branch:

```text
.agents/skills/pr-review/SKILL.md
```

```markdown
---
name: pr-review
description: Review pull requests in acme/payments.
---

# Pull request review

1. Read the full diff. Then read each changed function in full.
2. Find the callers of each changed function. Read them.
3. Treat a change to money rounding as a blocker candidate.
```

The skill takes effect for pull requests whose base commit contains it.

## Skael

Skael is an optional skill registry. Admins configure it on Settings → Skills.

| Field | Setting | Use |
|---|---|---|
| URL | `skael_url` | The Skael server. Empty turns Skael off. |
| Default set | `skael_default_set` | The set for repositories that choose no set. |
| Allowed sets | `skael_allowed_sets` | Sets that a repository can choose. |
| Review skill | `skael_review_skill` | The skill name in the set. Default `pr-review`. |
| Activation hook | `skael_activation_hook` | On by default. The runtime reports each skill activation to Skael. |
| Agent key | | A service account key with `skills:read`, `sets:read` and `events:write`. Limit it to one set when possible. |
| Reporter key | | A key with `events:write` and `outcomes:write`. |

The agent key is the only Skael credential in an agent run. The reporter key never leaves the API.

A repository uses the default set. To choose another set, set `skael_set` on the repository in the configuration YAML and apply it. See [operations.md](operations.md#configuration-as-yaml). The set must be the default set or an allowed set. Else the default set applies.

In a run, the runtime writes `~/.skael/config.json` with the URL and the agent key, and runs `skael pull --set <set>`. The result holds the set, the resolution ID and the skill versions.

After each round, the API reports to Skael with the reporter key:

- One event for each skill that the run loaded.
- One outcome: `succeeded`, `failed` or `cancelled`, with the count of steering signals, the verdict and a link to the round page.

When a steering signal arrives, and when the pull request closes, the API reports the outcome again with the new counts. A failed report is retried 5 times. The Diagnostics page then shows it.

## Extensions

An extension is a pi package with an exact version, for example `acme-pi-lint@1.4.0`. Admins manage extensions on Settings → Extensions.

| Field | Use |
|---|---|
| `spec` | The npm package with an exact version. |
| `env` | Environment values for the run. |
| secrets | Environment variables with secret values. |
| `enabled` | On or off. |
| `repositories` | The repositories that use it. Empty means all repositories. |

Sayso delivers the secrets of an extension only to runs of repositories that use the extension. These secrets reach the agent run.

The agent image installs the packages in `deploy/agent/extensions.lock` at build time. Add one exact `package@version` on each line. A package that is not in the file installs from the registry at the start of each run, and nothing caches it. The Extensions page shows a warning for it.

The agent session never loads project extensions, project settings or project MCP files from the pull request.

## Guidance layers

The runtime builds the prompt of each stage from these layers, in this order:

| Layer | Where | Limit |
|---|---|---|
| Contract | Part of the binary. It sets the role, the tool rules, the output rules and the security rules. You cannot edit it. | |
| Global guidance | Settings → Guidance. | 8,000 characters |
| Repository guidance | The repository page. | 4,000 characters |
| File guidance | `instructions` in `.sayso.yml`. | 4,000 characters |
| Skill | The resolved review skill. | |

Sayso keeps each version of the global and repository guidance. The newest version is active. The file guidance has the version of its commit.

The contract tells the agent that the later layers are review guidance only. Guidance cannot change the tools, the output format, the severity rules or the blocking rule.

The guidance editor shows the assembled prompt for a repository and a stage. Each round records the version of each layer and the SHA-256 of each assembled prompt. The steering queue can filter by these versions.

`AGENTS.md` and `CLAUDE.md` from the base commit are also context for the agent, marked as repository guidance.

## `.sayso.yml`

Put `.sayso.yml` in the repository root on the base branch. Sayso reads it from the base commit of each round. The file can narrow the repository settings and add guidance, within the limits that admins set.

```yaml
skill: .agents/skills/pr-review/SKILL.md
profiles: {review: review-strong}
mcp: [catalog]
extensions: [lint-helper]
feeds: [internal-npm]
ignore: ["**/*.gen.go", "vendor/**"]
instructions: |
  Treat changes to the ledger package as high risk.
nits: summary
block: true
max_inline_comments: 10
image: dotnet
toolchain: auto
restore: auto
setup: ["just generate"]
execution: off
```

| Key | Default | Rule |
|---|---|---|
| `skill` | `.agents/skills/pr-review/SKILL.md` | Path of the repository review skill. |
| `profiles` | repository profiles | A map of stage (`review`, `challenge`, `reply`, `fix`) to profile. The profile must be in `allowed_profiles`. |
| `mcp` | repository servers | Can only narrow the MCP servers of the repository. |
| `extensions` | enabled extensions | Can only narrow the extensions that are enabled for the repository. |
| `feeds` | repository feeds | Can only narrow the package feeds of the repository. |
| `ignore` | none | Paths that the agent must not report on. |
| `instructions` | none | File guidance for the review stage. Sayso cuts it at 4,000 characters. |
| `nits` | `summary` | `summary` or `inline`. `inline` posts `nit` findings as inline comments. |
| `block` | `true` | `false`: findings never block in this repository. |
| `max_inline_comments` | admin limit | 1 to the `max_inline_comments` setting (default 10). |
| `image` | repository image | A name from the agent image list on Settings → Images. |
| `toolchain` | `auto` | `auto` installs tool versions with mise. `none` turns it off. |
| `restore` | `auto` | `auto` restores dependencies. `none` turns it off. |
| `setup` | none | Commands that run after restore, inside the setup timeout. |
| `execution` | repository setting | Only `off` has an effect. It turns code execution off for the repository. |

The file cannot set these values. Admins and maintainers set them in the UI: `mode`, budgets, `override_teams`, provider settings, Skael settings, `max_concurrent_rounds` and the challenge threshold.

An unknown key, a UI-only key and a value outside the limits are ignored. The round page shows a warning for each one. The repository page shows the effective settings and the source of each value: `ui`, `file` or `default`.

### Code execution

Each repository has a code execution switch on its page. It is on by default.

- **On.** The agent can run commands in the workspace. With `toolchain: auto` and `restore: auto`, the runtime installs tool versions and restores dependencies first. The agent can write tests under `.sayso-tests/` and run them to prove a finding.
- **Off.** The agent gets only the read-only tools (read, grep, find, ls), the Sayso tools and the MCP tools. No toolchain, restore or setup step runs. A finding cannot use `test` evidence.

`execution: off` in `.sayso.yml` turns execution off. The file cannot turn execution on.

A failed toolchain, restore or setup step never fails the round. The agent then uses `trace` or `call_path` evidence.
