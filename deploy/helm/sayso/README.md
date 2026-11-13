# Sayso Helm chart

The chart installs the `api` and `worker` roles of Sayso, the RBAC that the worker needs to run agent Jobs, and the NetworkPolicy of agent Jobs. It can also install the relay and a read-only Temporal UI.

The chart runs no Temporal server and no PostgreSQL.

## Before you install

1. Get a PostgreSQL 16 database.
2. Get a Temporal server. Use an existing cluster, or install the upstream Temporal chart: https://github.com/temporalio/helm-charts.
3. Create a Secret with the keys `SAYSO_DATABASE_URL` and `SAYSO_SECRET_KEY`. Add `SAYSO_SETUP_TOKEN` for the first setup.

```sh
kubectl create secret generic sayso \
  --from-literal=SAYSO_DATABASE_URL='postgresql://sayso:...@postgres:5432/sayso' \
  --from-literal=SAYSO_SECRET_KEY="$(openssl rand -base64 32)" \
  --from-literal=SAYSO_SETUP_TOKEN="$(openssl rand -hex 16)"
```

## Install

```sh
helm install sayso oci://ghcr.io/alternayte/charts/sayso \
  --set baseURL=https://sayso.example.test \
  --set temporal.address=temporal-frontend.temporal:7233
```

## Agent egress

A NetworkPolicy cannot name hosts. Put the CIDR blocks of GitHub and of your registries in `agentNetworkPolicy.egressCIDRs`. Agent Jobs can always reach the API and DNS.

## Temporal UI

Set `temporalUi.enabled=true` to run a read-only Temporal UI. The API proxies it at `/temporal` for admins. The chart never creates an Ingress for it.
