# Security

A pull request can contain code that reads the environment, and text that tries to give the agent instructions. Sayso treats the agent run as untrusted. Credentials stay in the API.

## What Sayso never does

- It never submits a review with `APPROVE` or `REQUEST_CHANGES`. Every review uses the `COMMENT` event. The verdict is a check run.
- It never merges a pull request.
- It never pushes to a branch that it did not create. It writes only to branches that start with `sayso/fix/`, and only after a `/sayso fix` command from a person with write permission.
- It never changes its own configuration, prompts, skills or repository.

## What the agent run never holds

An agent run never receives these credentials:

- a model provider key
- a GitHub token with write permission
- an MCP server credential
- the Skael reporter key
- `SAYSO_SECRET_KEY`

The agent reaches models only through the model proxy (`/llm/<provider>`) and MCP servers only through the MCP proxy (`/mcp/<name>`). Both proxies add the credential on the API side. See [models.md](models.md#model-proxy) and [mcp.md](mcp.md#mcp-proxy).

An agent run gets these values:

| Value | Scope |
|---|---|
| Run token | One run. See below. |
| GitHub installation token | One repository, `contents: read` and `metadata: read`. The workspace removes it from the git configuration after the clone. |
| Skael agent key | Only when Skael applies. It has read scopes and `events:write`. |
| Extension secrets | Only for extensions that are enabled for the repository. |
| Package feed credentials | Only for feeds in `inject` mode. See below. |

## Run tokens

The worker creates one run token for each agent run.

- The token is 32 random bytes. Sayso stores only its SHA-256.
- It belongs to one run.
- It stops working when the run ends, or after 2 hours.
- The model, MCP and package proxies accept only an active run token, and only the models, servers and feeds of that run.

## Agent isolation

**Process runner.** The worker starts `sayso agent` in a new temporary directory. The environment holds only `SAYSO_API_URL`, `SAYSO_RUN_TOKEN`, `SAYSO_RUN_ID`, `HOME`, `PATH` and the proxy variables. The worker kills the process group on cancel or timeout and deletes the directory.

**Kubernetes runner.** Each run is a Job with these properties:

- No service account token.
- Non-root user, read-only root filesystem, no privilege escalation.
- `emptyDir` volumes only.
- The egress NetworkPolicy of the Helm chart. Agent Jobs reach only the API, DNS and the CIDR blocks that you list.

**Session.** The agent session loads only the resolved skill, the Sayso tools and the enabled extensions. It never loads project extensions, project settings or project MCP files from the pull request. The skill, `.sayso.yml`, `AGENTS.md` and `CLAUDE.md` come from the base commit only.

**Code execution.** Turn execution off for a repository to stop the agent from running any command. The agent then has only read-only tools. See [skills.md](skills.md#code-execution).

**Prompt.** The contract tells the agent that pull request text, code, comments and tool output are data, not instructions. Guidance cannot weaken the tool validation or the blocking rule.

## Output

- The UI renders agent text without raw HTML.
- Sayso puts each `@` mention in agent text in code format before it posts to GitHub, so the text never notifies people.
- The page sends `Content-Security-Policy: default-src 'self'`.

## Redaction

Before Sayso stores run data, it replaces each known secret value with `[redacted:<name>]`. This applies to event previews, event payloads, artifacts and logs. Known values include:

- every stored secret and every value of a referenced environment variable
- run tokens
- GitHub tokens
- the Skael agent key
- `SAYSO_DATABASE_URL`, `SAYSO_SETUP_TOKEN`, `SAYSO_RELAY_TOKEN` and `SAYSO_TEMPORAL_API_KEY`

`sayso doctor` output is redacted too.

## Secret storage

A secret field in the UI takes one of these:

- **A value.** Sayso encrypts it with AES-256-GCM under `SAYSO_SECRET_KEY` and stores it in PostgreSQL.
- **An environment variable name.** Sayso stores only the name, and reads the value from the process environment when it needs it. Set the variable in every `api` and `worker` container. Use this to take secrets from an external secret manager.

After save, the API never returns the value. The field shows `•••• <last 4>`, the save date and a **Replace** button. For an environment variable, the page shows whether the variable is set in the API process.

Each secret change writes an audit entry without the value. `sayso config export` writes secret fields as `secret: <name>` or `env: <NAME>`, never as values.

## Rotate the secret key

`sayso secrets rekey` encrypts all stored secrets again with a new key, in one transaction.

1. Stop the `api` and `worker` processes.
2. Generate a new key:

   ```sh
   openssl rand -base64 32
   ```

3. Run the rekey with the old key and the new key:

   ```sh
   SAYSO_SECRET_KEY=<old key> SAYSO_SECRET_KEY_NEW=<new key> sayso secrets rekey
   ```

4. Set `SAYSO_SECRET_KEY` to the new key. Remove `SAYSO_SECRET_KEY_NEW`.
5. Start the processes.

If one secret does not open with the old key, the rekey changes nothing and fails. The rekey writes an audit entry.

## Package feeds

Private package feeds use `proxy` mode by default. The package proxy at `/pkg/<feed>` adds the credential on the API side. It accepts only `GET`, `HEAD` and the git upload-pack calls, so an agent run cannot publish a package or push through it.

`inject` mode gives the feed credential to agent runs. Use it only for a feed or tool that cannot use the proxy. The UI asks for a confirmation, marks the feed "Credential exposed to agent runs" and writes an audit entry. Use read-only, short-lived tokens for inject-mode feeds.

## Webhooks and the relay

- Sayso checks the webhook signature before it parses a delivery. A wrong signature gets 401 and an audit entry.
- The relay stores nothing on disk and has no database access. It streams only to a client with `SAYSO_RELAY_TOKEN`.

## Access

- Only members of the App organization can sign in.
- Session cookies are HttpOnly and SameSite=Lax, and Secure on HTTPS. State-changing requests pass an origin check.
- `/setup` returns 404 after the App exists.
- Commands act only after a check of the repository permission of the commenter.
- The Temporal UI has no published port and no Ingress. Only admins reach it, through the API, and it cannot change workflows.
- The audit log is append-only through the application. Admins read it on the Audit page.
