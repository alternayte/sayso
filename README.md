# Sayso

Sayso is a self-hosted GitHub pull request reviewer. It runs an agent with skills and MCP tools against the whole repository, not only the diff. It posts a verdict, a risk level, a guide for human reviewers and targeted comments. It keeps a ledger of findings, so it does not raise an issue again after a person answers it.

People keep the final decision. Sayso never approves, never requests changes and never merges.

## How it works

- One Go binary, `sayso`, has the roles `api`, `worker`, `relay` and `all`.
- The `sayso-agent` image holds the Node agent runtime. Each agent run executes in a runner: a subprocess (`process`) or a Kubernetes Job (`kubernetes`).
- PostgreSQL 16 or later is the only application datastore. Temporal runs one workflow for each pull request.
- The agent reaches models and MCP servers only through proxies in the API. It never holds a model key, a GitHub write token or an MCP credential.

Each push to a pull request starts a round. A round has these stages:

1. `review`: the agent reads the change, its callers, consumers and tests, and records findings.
2. `challenge`: a second session with the `challenge` profile tries to refute each candidate. Only a confirmed blocker with evidence can block.

Sayso then publishes a summary comment, inline review comments with the `COMMENT` event, and a check run named `sayso`.

## Modes

Set the mode of each repository on its page in the UI. New repositories start in `off`.

| Mode | Agent runs | GitHub writes | Check run |
|---|---|---|---|
| `off` | No | No | No |
| `shadow` | Yes | No. The UI shows what Sayso would post. | No |
| `comment` | Yes | Summary, review comments, replies | Yes. `blocking` gives `neutral`. |
| `gate` | Yes | Summary, review comments, replies | Yes. `blocking` gives `failure`. |

## Commands

Write a command as a pull request comment that starts with `/sayso`. Sayso checks the repository permission of the commenter first.

| Command | Permission | Result |
|---|---|---|
| `/sayso review` | write | Starts a round on the head commit now. |
| `/sayso pause`, `/sayso resume` | write | Stops or starts automatic rounds on this pull request. |
| `/sayso dismiss S-<n> <reason>` | write | Dismisses the finding and resolves its thread. |
| `/sayso override <reason>` | maintain, or a team in the `override_teams` setting | Sets the check to `success` for the current head commit. |
| `/sayso fix S-<n>` | write | Opens a fix pull request from `sayso/fix/<pr>-<n>` into the head branch. Same-repository branches only. |
| `/sayso explain S-<n>` | read | Replies in the thread with more detail. |

A reply in a Sayso thread starts a reply run. The agent concedes, holds or clarifies. The `sayso:skip` label also stops automatic rounds.

## Quick start with Docker Compose

You need Docker with Compose, and a URL that GitHub can reach for webhooks.

1. Download the Compose file and the example environment file:

   ```sh
   mkdir sayso && cd sayso
   curl -fsSLO https://raw.githubusercontent.com/alternayte/sayso/main/deploy/compose/compose.yaml
   curl -fsSL -o .env https://raw.githubusercontent.com/alternayte/sayso/main/deploy/compose/.env.example
   ```

2. Generate the secret key and the setup token, and write them in `.env`:

   ```sh
   echo "SAYSO_SECRET_KEY=$(openssl rand -base64 32)" >> .env
   echo "SAYSO_SETUP_TOKEN=$(openssl rand -hex 16)" >> .env
   ```

   Remove the empty `SAYSO_SECRET_KEY=` and `SAYSO_SETUP_TOKEN=` lines of the example file.

3. Set `SAYSO_BASE_URL` to the URL where people open Sayso. Set `SAYSO_BOOTSTRAP_ADMINS` to your GitHub login, for example `octo-admin`.

4. Start Sayso:

   ```sh
   docker compose up -d
   ```

5. Open `SAYSO_BASE_URL/setup`. Type the setup token. Create the GitHub App. Then follow the setup checklist.

The API listens on `127.0.0.1:8080`. Put a reverse proxy with TLS in front of it, or use the relay. See [docs/install.md](docs/install.md).

## Documentation

- [Install](docs/install.md): Docker Compose, the Helm chart, environment variables and first-run setup.
- [GitHub App](docs/github-app.md): App creation, permissions, webhooks, the relay, sign-in and roles.
- [Models](docs/models.md): providers, profiles, the model proxy, budgets and fair scheduling.
- [MCP](docs/mcp.md): MCP servers, the MCP proxy and tool allowlists.
- [Skills](docs/skills.md): skill resolution, Skael, extensions, guidance and `.sayso.yml`.
- [Security](docs/security.md): the security model, run tokens, redaction and secret storage.
- [Operations](docs/operations.md): storage, metrics, the Temporal UI, `sayso doctor`, configuration as YAML, upgrades and backups.

## Build from source

You need Go, Bun, Node 22 and `just`.

```sh
just setup
just build
bin/sayso version
```

`just` lists all recipes. `just check` runs the full gate and needs Docker.

## Licence

MIT.
