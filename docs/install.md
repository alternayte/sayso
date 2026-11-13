# Install

Sayso installs with Docker Compose on one host, or with the Helm chart on Kubernetes. Both use the same two images:

- `ghcr.io/alternayte/sayso`: the `sayso` binary.
- `ghcr.io/alternayte/sayso-agent`: the agent runtime and the `sayso` binary.

## Requirements

- PostgreSQL 16 or later. It holds all application state.
- A Temporal server. It can use the same PostgreSQL server with separate databases.
- A URL that GitHub can reach for webhooks: the API, or the relay.
- A GitHub organization on github.com.

Containers are stateless. Only PostgreSQL needs a volume.

## Roles

One binary runs in one of four roles. Set the role with `sayso serve --role <role>` or with `SAYSO_ROLE`. The flag wins.

| Role | Does |
|---|---|
| `api` | Serves the UI, the REST API, webhooks, the runner API, the model proxy, the MCP proxy and the package proxy. |
| `worker` | Runs the Temporal workflows and activities, and starts agent runs in the runner. |
| `relay` | Receives webhooks on a public host and streams them to the API. It has no database. |
| `all` | `api` and `worker` in one process. This is the default. |

You can run more than one replica of `api` and `worker`.

## Environment variables

All other configuration lives in the database. Set it in the UI.

| Variable | Default | Use |
|---|---|---|
| `SAYSO_DATABASE_URL` | | PostgreSQL URL. Required, except for the relay. |
| `SAYSO_SECRET_KEY` | | 32 random bytes in base64. Required, except for the relay. It encrypts stored secrets. |
| `SAYSO_SECRET_KEY_NEW` | | The new key, for `sayso secrets rekey` only. |
| `SAYSO_BASE_URL` | | Public URL of the UI and API. Sign-in callbacks and links in comments use it. |
| `SAYSO_PUBLIC_WEBHOOK_URL` | `SAYSO_BASE_URL/webhooks/github` | URL that GitHub calls: the API in `direct` mode, the relay in `relay` mode. |
| `SAYSO_INGRESS` | `direct` | `direct` or `relay`. |
| `SAYSO_RELAY_URL` | | URL of the relay, for the API in `relay` mode. |
| `SAYSO_RELAY_TOKEN` | | Shared token of the API and the relay. |
| `SAYSO_ROLE` | `all` | `all`, `api`, `worker` or `relay`. |
| `SAYSO_LISTEN` | `:8080` | Listen address. |
| `SAYSO_SETUP_TOKEN` | | Gives access to the setup page before the GitHub App exists. |
| `SAYSO_BOOTSTRAP_ADMINS` | | GitHub logins, separated by commas. Each one gets `admin` at first sign-in. |
| `SAYSO_TEMPORAL_ADDRESS` | `localhost:7233` | Temporal frontend address. |
| `SAYSO_TEMPORAL_NAMESPACE` | `default` | Temporal namespace. |
| `SAYSO_TEMPORAL_TLS_CERT`, `SAYSO_TEMPORAL_TLS_KEY` | | Client certificate and key files for Temporal mTLS. |
| `SAYSO_TEMPORAL_API_KEY` | | Temporal API key. |
| `SAYSO_TEMPORAL_UI_URL` | | Internal URL of a read-only Temporal UI. The API proxies it at `/temporal` for admins. |
| `SAYSO_RUNNER` | `process` | `process` or `kubernetes`. |
| `SAYSO_RUNNER_API_URL` | `SAYSO_BASE_URL` | API URL as agent runs see it. |
| `SAYSO_K8S_NAMESPACE` | | Namespace of agent Jobs. Required for the `kubernetes` runner. |
| `SAYSO_AGENT_IMAGE` | | Image of agent Jobs for the `kubernetes` runner. |
| `SAYSO_EXTRA_CA_PEM`, `SAYSO_EXTRA_CA_FILE` | | Extra CA certificates for outbound HTTPS, as PEM text or as a file. |
| `SAYSO_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` | | Outbound proxy. All outbound HTTP uses it. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | | Turns on OTLP trace export. The exporter reads the standard `OTEL_*` variables. |

Startup fails with a clear message when a required variable is missing. An unknown `SAYSO_*` variable logs a warning.

Generate the secret key and the setup token:

```sh
openssl rand -base64 32   # SAYSO_SECRET_KEY
openssl rand -hex 16      # SAYSO_SETUP_TOKEN
```

Keep a copy of `SAYSO_SECRET_KEY` outside the database. Without it, Sayso cannot open the stored secrets.

## Docker Compose

`deploy/compose/compose.yaml` runs Sayso on one host from the released images. It needs no checkout.

```sh
mkdir sayso && cd sayso
curl -fsSLO https://raw.githubusercontent.com/alternayte/sayso/main/deploy/compose/compose.yaml
curl -fsSL -o .env https://raw.githubusercontent.com/alternayte/sayso/main/deploy/compose/.env.example
```

Edit `.env`. Set at least these values:

```sh
SAYSO_SECRET_KEY=<openssl rand -base64 32>
SAYSO_SETUP_TOKEN=<openssl rand -hex 16>
SAYSO_BASE_URL=https://sayso.example.test
SAYSO_BOOTSTRAP_ADMINS=octo-admin
POSTGRES_PASSWORD=<a strong password>
```

Start the services:

```sh
docker compose up -d
docker compose ps
```

The file has these services:

| Service | Does |
|---|---|
| `postgres` | PostgreSQL 16 with a named volume. It holds the Sayso database and the two Temporal databases. |
| `temporal` | Temporal on the same PostgreSQL. It registers the `default` namespace with 7 days retention. |
| `temporal-retention` | Sets the 7 days retention on an existing namespace, once. |
| `temporal-ui` | The read-only Temporal UI. It has no published port. |
| `migrate` | Applies the database migrations once, before `sayso` and `worker` start. |
| `sayso` | The `api` role on `127.0.0.1:${SAYSO_PORT:-8080}`. |
| `worker` | The `worker` role in the agent image, with the `process` runner. |
| `relay` | Profile `relay`. The relay role on `127.0.0.1:${SAYSO_RELAY_PORT:-8081}`. |
| `fake` | Profile `fakes`. Fake GitHub and model servers, for demos and tests only. |

Other `.env` values: `SAYSO_PORT`, `SAYSO_IMAGE`, `SAYSO_AGENT_IMAGE`, `TEMPORAL_VERSION`, `TEMPORAL_UI_VERSION`, `SAYSO_LOG_LEVEL`, `SAYSO_INGRESS`, `SAYSO_RELAY_URL`, `SAYSO_RELAY_TOKEN`, `SAYSO_PUBLIC_WEBHOOK_URL`.

The API port binds to `127.0.0.1` only. To receive webhooks, do one of these:

- Put a reverse proxy with TLS in front of port 8080. Set `SAYSO_BASE_URL` to its URL.
- Use the relay. See [github-app.md](github-app.md#relay-ingress).

## Helm chart

The chart is `deploy/helm/sayso`. It installs:

- Deployments for `api` and `worker`, and a Service for the API.
- A ServiceAccount, Role and RoleBinding that let the worker manage Jobs and Pods in the runner namespace.
- A NetworkPolicy for agent Jobs.
- Optional: the relay, an Ingress for the API, an Ingress for the relay, and a read-only Temporal UI.

The chart runs no PostgreSQL and no Temporal server. Use an existing Temporal cluster, or install the upstream Temporal chart (https://github.com/temporalio/helm-charts).

1. Create a Secret with the required values:

   ```sh
   kubectl create secret generic sayso \
     --from-literal=SAYSO_DATABASE_URL='postgresql://sayso:<password>@postgres:5432/sayso' \
     --from-literal=SAYSO_SECRET_KEY="$(openssl rand -base64 32)" \
     --from-literal=SAYSO_SETUP_TOKEN="$(openssl rand -hex 16)"
   ```

   The Secret can also hold `SAYSO_RELAY_TOKEN` and `SAYSO_TEMPORAL_API_KEY`. The pods load all keys of the Secret as environment variables.

2. Install the chart:

   ```sh
   helm install sayso oci://ghcr.io/alternayte/charts/sayso \
     --set baseURL=https://sayso.example.test \
     --set temporal.address=temporal-frontend.temporal:7233 \
     --set bootstrapAdmins=octo-admin \
     --set ingress.enabled=true \
     --set ingress.host=sayso.example.test
   ```

The chart sets `SAYSO_RUNNER=kubernetes`. Agent runs are Jobs in `runner.namespace`, or in the release namespace when it is empty.

### Values

| Value | Default | Use |
|---|---|---|
| `image.repository`, `image.tag` | `ghcr.io/alternayte/sayso`, chart version | Image of `api`, `worker` and `relay`. |
| `agentImage.repository`, `agentImage.tag` | `ghcr.io/alternayte/sayso-agent`, chart version | Image of agent Jobs. |
| `baseURL` | `http://localhost:8080` | `SAYSO_BASE_URL`. |
| `publicWebhookURL` | empty | `SAYSO_PUBLIC_WEBHOOK_URL`. Empty gives `<baseURL>/webhooks/github`. |
| `ingressMode` | `direct` | `direct` or `relay`. |
| `existingSecret` | `sayso` | Secret with the secret variables. |
| `bootstrapAdmins` | empty | `SAYSO_BOOTSTRAP_ADMINS`. |
| `temporal.address`, `temporal.namespace` | `temporal-frontend:7233`, `default` | Temporal connection. |
| `temporal.tlsSecret` | empty | Secret with `tls.crt` and `tls.key` for Temporal mTLS. |
| `api.replicas`, `worker.replicas` | `1` | Replica counts. |
| `runner.namespace` | empty | Namespace of agent Jobs. |
| `ingress.enabled`, `ingress.host`, `ingress.className`, `ingress.tls` | off | Ingress for the API. |
| `relay.enabled`, `relay.ingress.*` | off | The relay and its Ingress. The Ingress exposes only `/webhooks/github`. |
| `temporalUi.enabled` | off | A read-only Temporal UI behind the API at `/temporal`. It never gets an Ingress. |
| `agentNetworkPolicy.enabled` | on | The egress NetworkPolicy of agent Jobs. |
| `agentNetworkPolicy.egressCIDRs` | empty | CIDR blocks that agent Jobs can reach on port 443. |

### Agent egress

Agent Jobs can always reach the API and DNS. A NetworkPolicy cannot name hosts. Put the CIDR blocks of GitHub and of your package registries in `agentNetworkPolicy.egressCIDRs`.

## First-run setup

1. Open `SAYSO_BASE_URL/setup`.
2. Type the value of `SAYSO_SETUP_TOKEN`. The setup session lasts 2 hours.
3. Type the name of your GitHub organization, for example `acme`, and create the GitHub App. See [github-app.md](github-app.md).
4. Sign in with GitHub. Sign in with a login in `SAYSO_BOOTSTRAP_ADMINS` to get `admin`.
5. Follow the setup checklist on the home page.

After the App exists, `/setup` returns 404. The setup token then has no use. You can remove it.

To give a person `admin` from the command line:

```sh
sayso admin grant --github-login octo-admin --role admin
```

### Setup checklist

Admins see the checklist on the home page until all steps are done. Each step links to its page.

1. The GitHub App exists.
2. The App is installed on one or more repositories.
3. Sayso received a webhook delivery.
4. One provider passes the smoke test. See [models.md](models.md).
5. The four default profiles `review`, `challenge`, `reply` and `fix` have targets.
6. A storage test passed. See [operations.md](operations.md#storage).
7. One repository is in `shadow` mode or higher.
8. The first round is done.

Start each repository in `shadow` mode. Read the rounds in the UI. Change to `comment` or `gate` when the output is useful.

## Check the install

```sh
curl -fsS https://sayso.example.test/readyz
docker compose run --rm sayso doctor
```

`/readyz` checks the database, the migrations and Temporal. See [operations.md](operations.md#sayso-doctor) for `sayso doctor`.
