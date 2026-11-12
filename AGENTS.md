# AGENTS.md

## What this is
Sayso is a self-hosted GitHub pull request reviewer that runs an agent with skills and MCP tools against the whole repository and posts a verdict, a risk level and targeted comments.
One Go binary serves the api, worker and relay roles, and a separate agent image runs the Node agent runtime.

## Run
- The repository holds no code yet. The build adds the justfile, and this section lists its recipes after that.

## Test
- No gate exists yet. The build adds just ci as the gate. This repository has no just check.
- `checks/agents-md.sh` checks this file.

## Stack rules
- The repository is public. Specs, the build prompt, the ledger and decisions live in the private build directory, .build, which is its own git repository. A clone without it still builds.
- Never name a private file in a commit, a PR or a tracked file.
- Tracked files and commit messages contain no employer names, internal hostnames, people or real data. Test data uses only the synthetic acme fixtures.
- Workflows run on Temporal. PostgreSQL is the only application datastore, and workflow payloads hold IDs only.
- The UI tooling uses Bun. The agent runtime uses Node 22.
- The agent process never holds a model key, a GitHub write token or an MCP credential. It reaches models and MCP servers only through the API proxies with a run token.
- Sayso never submits APPROVE or REQUEST_CHANGES, never merges, and never pushes to a branch it did not create.
- Licence is MIT. Add no GPL, AGPL, SSPL or BSL dependency.
- Write docs, UI copy, bot comments and commit messages in ASD-STE100 style.

## Domain words
- review: the long-lived record and workflow of one pull request.
- round: one review of one head commit, with one agent run. Avoid: job.
- agent run: one execution of the agent runtime in a runner. Avoid: bot run.
- stage: one agent session inside an agent run.
- finding: one issue the agent reports, with severity, location and evidence.
- ledger: all findings of a review with their status history.
- verdict: the outcome of a round, published as a check run.
- risk: the risk level of a change, with reasons.
- provider: a model endpoint with API type, base URL, key and headers.
- profile: a named model choice for a stage, an ordered list of provider and model targets with parameters.
- steering signal: a human action that shows the agent was wrong or incomplete.
- mode: the per-repository setting that controls what Sayso publishes.
