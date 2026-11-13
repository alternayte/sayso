# Operations

## Health and readiness

| Route | Roles | Use |
|---|---|---|
| `/healthz` | all | 200 while the process runs. |
| `/readyz` | all | 200 when the database is reachable, all migrations are applied and Temporal is healthy. Else 503 with a JSON body that names the failed part. |
| `/metrics` | `api`, `worker`, `all` | Prometheus metrics. |

## Metrics

| Metric | Labels | Use |
|---|---|---|
| `sayso_http_requests_total` | `route`, `method`, `status` | HTTP requests. |
| `sayso_http_request_duration_seconds` | `route`, `method` | HTTP request durations. |
| `sayso_webhook_signal_seconds` | | Time from webhook request start to the workflow signal. |
| `sayso_webhook_deliveries_total` | `result` | Webhook deliveries by result. |
| `sayso_rounds` | `status` | Rounds by status. |
| `sayso_agent_run_duration_seconds` | `kind` | Durations of finished agent runs. |
| `sayso_model_proxy_requests_total` | `provider`, `result` | Model proxy requests. |
| `sayso_model_tokens_total` | `direction` | Model tokens through the model proxy. |
| `sayso_model_cost_usd_total` | | Model cost through the model proxy in USD. |
| `sayso_proxy_errors_total` | `class` | Model proxy calls that ended with a client or server error. |
| `sayso_steering_signals_total` | `type` | Steering signals by type. |

Set `OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` to export traces over OTLP. The exporter reads the standard `OTEL_*` variables.

Logs are JSON on standard error, with secret redaction. Set the level with `SAYSO_LOG_LEVEL`.

## Storage

The event index of each run is always in PostgreSQL. Payloads go to the default store: model messages, tool input and output, the session file, `result.json` and `manifest.json`. Admins manage stores on Settings → Storage.

| Driver | Config | Secret |
|---|---|---|
| `postgres` | none. Objects go to the `blobs` table. This is the default store of a new install. | none |
| `s3` | `endpoint` (host and port), `region`, `bucket`, `path_style`, `insecure` (HTTP instead of HTTPS), `access_key`, `prefix` | The secret access key. Without `access_key`, the default AWS credential chain applies. |
| `azblob` | `account_url`, `container`, `auth` (`key`, `connection_string` or `default`), `prefix` | The account key, or the connection string. With `auth: default`, the default Azure credential applies. |

Click **Test** to write, read and delete a test object.

```yaml
name: runs
driver: s3
config:
  endpoint: s3.eu-west-1.amazonaws.com
  region: eu-west-1
  bucket: acme-sayso-runs
  access_key: AKIAEXAMPLE
secret:
  env: SAYSO_S3_SECRET_ACCESS_KEY
retention_days: 90
default: true
```

New objects go to the default store. Each object records its store, so old objects stay readable after you change the default. You cannot delete a store that is the default or that still holds objects.

The package cache of proxy-mode feeds also uses the default store.

### Retention

A daily Temporal schedule, `sayso-retention`, does these steps:

1. It deletes payloads and artifacts older than the `retention_days` of their store. The default is 90 days. The limit is 1 to 3650 days.
2. It deletes event index rows older than the `event_index_retention_days` setting. The default is 400 days.

## The Temporal UI

Sayso runs one Temporal workflow for each pull request. Operators can read workflows in the Temporal UI.

- The Temporal UI runs read-only (`TEMPORAL_DISABLE_WRITE_ACTIONS=true`) with no published port.
- When `SAYSO_TEMPORAL_UI_URL` is set, the API proxies `/temporal/*` to it for admins. Other users get 404.
- Compose sets `SAYSO_TEMPORAL_UI_URL=http://temporal-ui:8080`. In Helm, set `temporalUi.enabled=true`.
- Review and round pages show admins a link to the workflow.

Cancel rounds in the Sayso UI. A cancel from the Temporal CLI gets the same cleanup: the agent run stops, and the check becomes `neutral`.

`sayso serve` registers the Temporal namespace with 7 days retention when it is missing. Set the `register_temporal_namespace` setting to `false` to turn this off.

## `sayso doctor`

`sayso doctor` checks the dependencies and prints one line for each check. The Diagnostics page shows the same checks.

```sh
sayso doctor
sayso doctor --json
```

| Check | Does |
|---|---|
| `database` | The PostgreSQL version and the migration state. |
| `secret_key` | All stored secrets open with `SAYSO_SECRET_KEY`. |
| `temporal` | The Temporal connection and the namespace. |
| `github_app` | The App credentials and an installation token mint. |
| `webhook_ingress` | The ingress mode, the last delivery and, in `relay` mode, the relay stream state. The CLI holds no stream, so it checks that the relay answers. |
| `runner` | The process runner binary, or the Kubernetes permissions. |
| `outbound_proxy` | The proxy variables and the extra CA certificates. |
| `provider <name>` | Each model provider. |
| `mcp <name>` | Each MCP server. |
| `store <name>` | Each store: a write, a read and a delete. |
| `skael` | Skael is off, or the server answers and the agent key is stored. |

Other tests are buttons in the UI. Settings → Agent images has the runner test: it starts a no-op agent run and shows the harness version. Settings → Skills has the Skael test: a no-op agent run that pulls the default set. Settings → GitHub lists the deliveries of the last 24 hours on GitHub and can ask GitHub to send one again.

The command exits with a non-zero status when a check fails. It needs `SAYSO_DATABASE_URL`, `SAYSO_SECRET_KEY` and the Temporal variables. Its output is redacted.

## Settings

Admins change settings on Settings → General. Sayso keeps each version with the author, the time, and the old and new values.

| Setting | Default | Use |
|---|---|---|
| `debounce_seconds` | 90 | Wait after a push before a round starts. |
| `reconcile_interval_minutes` | 10 | Interval of the reconciler. |
| `worker_max_agent_runs` | 4 | Agent runs at the same time. |
| `max_concurrent_rounds` | 2 | Rounds at the same time in one repository. |
| `round_timeout_minutes` | 30 | Time limit of a round agent run. |
| `reply_timeout_minutes` | 5 | Time limit of a reply run. |
| `setup_timeout_minutes` | 10 | Shared time limit of toolchain, restore and setup steps. |
| `max_findings_per_round` | 25 | Findings in one round. |
| `max_tool_calls_per_stage` | 200 | Tool calls in one stage. |
| `max_inline_comments` | 10 | Inline comments in one round. |
| `match_similarity` | 0.6 | Title similarity at which a new finding repeats an old one. |
| `monthly_budget_usd` | 0 | Monthly model budget. 0 means no limit. |
| `override_teams` | `[]` | Teams that can use `/sayso override`. |
| `allowed_profiles` | `["review","challenge","reply","fix"]` | Profiles that repositories can choose. |
| `event_index_retention_days` | 400 | Retention of the event index. |
| `register_temporal_namespace` | `true` | Register the Temporal namespace at start when it is missing. |
| `skael_url`, `skael_default_set`, `skael_allowed_sets`, `skael_review_skill`, `skael_activation_hook` | | Skael. See [skills.md](skills.md#skael). |

## Configuration as YAML

PostgreSQL holds the configuration. You can export it as YAML, keep it in version control and apply it again.

```sh
sayso config export --file sayso-config.yaml
sayso config apply --file sayso-config.yaml
sayso config apply --file sayso-config.yaml --yes
```

- `export` writes the settings, providers, profiles, MCP servers, extensions, package feeds, stores, agent images, repositories and team roles. Without `--file`, it writes to standard output.
- `apply` shows the diff and asks for a confirmation. Then it applies all changes in one transaction. `--yes` skips the confirmation.
- Secret fields are `secret: <name>` (a stored secret) or `env: <NAME>` (an environment variable). The file never holds a secret value.
- An apply that names a missing stored secret fails and changes nothing.
- A section that is not in the file stays unchanged. A list section in the file is complete: apply removes the entries that it does not list. Apply never removes a repository.
- In `settings`, apply changes only the keys that the file lists.

```yaml
version: 1
settings:
  debounce_seconds: 90
  monthly_budget_usd: 500
providers:
  - name: anthropic
    api_type: anthropic-messages
    base_url: https://api.anthropic.com/v1
    auth_style: x_api_key
    key:
      env: ANTHROPIC_API_KEY
    headers:
      anthropic-version: "2023-06-01"
    compat: {}
    models: []
    max_concurrency: 4
    tokens_per_minute: 0
repositories:
  - full_name: acme/payments
    mode: shadow
    profiles: {review: review, challenge: challenge, reply: reply, fix: fix}
    skael_set: ""
    agent_image: ""
    mcp_servers: [catalog]
    feeds: []
    execution: true
    challenge_min_severity: minor
team_roles:
  - team: payments-maintainers
    role: maintainer
```

Settings → Config in the UI has the same export, and an import page that shows the diff.

## Upgrades and migrations

The binary holds all database migrations.

- `sayso serve` applies pending migrations at start, under a PostgreSQL advisory lock. Many processes can start at once.
- `sayso migrate` applies pending migrations and exits. The Compose `migrate` service runs it before `sayso` and `worker` start.
- `/readyz` returns 503 while migrations are pending.

To upgrade Compose:

```sh
docker compose pull
docker compose up -d
```

To upgrade Helm:

```sh
helm upgrade sayso oci://ghcr.io/alternayte/charts/sayso --reuse-values --version <new version>
```

Reviews that are in progress continue after an upgrade. Workflow code changes use Temporal worker versioning.

On SIGTERM, a process stops taking work, finishes requests, lets the Temporal workers drain and exits within 30 seconds.

## Backups

All state is in PostgreSQL, and in the object store when you use `s3` or `azblob`. Containers hold no state.

Back up these items:

1. The Sayso database.
2. The Temporal databases. In Compose they are `temporal` and `temporal_visibility` on the same server.
3. The bucket or container of each `s3` and `azblob` store.
4. `SAYSO_SECRET_KEY`. Keep it apart from the database backup. Without it, the stored secrets do not open.

```sh
docker compose exec -T postgres pg_dump -U sayso -Fc sayso > sayso.dump
docker compose exec -T postgres pg_dump -U sayso -Fc temporal > temporal.dump
docker compose exec -T postgres pg_dump -U sayso -Fc temporal_visibility > temporal_visibility.dump
```

`sayso config export` is not a backup. It holds no secrets, findings, rounds or run records.
