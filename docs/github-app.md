# GitHub App

Sayso acts on GitHub as a GitHub App in your organization. The App also gives people sign-in with GitHub.

## Create the App

Sayso creates the App through the GitHub App manifest flow. You do not type any App credential.

1. Set `SAYSO_BASE_URL` to the public URL of Sayso.
2. Set `SAYSO_PUBLIC_WEBHOOK_URL` when GitHub must call another URL, for example the relay. When it is empty, Sayso uses `SAYSO_BASE_URL/webhooks/github`.
3. Open `SAYSO_BASE_URL/setup` and type the setup token.
4. Type the organization name, for example `acme`.
5. Sayso sends you to `https://github.com/organizations/acme/settings/apps/new`. Confirm the App on GitHub.
6. GitHub sends you back to `/setup/github/callback`. Sayso stores the App ID, the client ID, the client secret, the private key and the webhook secret as encrypted secrets.
7. Sign in with GitHub. Then open the install link on Settings → GitHub and install the App on the repositories to review.

The manifest sets these values:

| Field | Value |
|---|---|
| Name | `Sayso <org>` |
| Homepage URL | `SAYSO_BASE_URL` |
| Webhook URL | `SAYSO_PUBLIC_WEBHOOK_URL` |
| Setup redirect | `SAYSO_BASE_URL/setup/github/callback` |
| Sign-in callback | `SAYSO_BASE_URL/auth/github/callback` |
| Public | No |

You can create only one App. After it exists, `/setup` returns 404.

## Permissions

| Scope | Permission | Use |
|---|---|---|
| Repository | Contents: write | Fix branches only. Sayso writes only to branches that start with `sayso/fix/`. |
| Repository | Checks: write | The `sayso` check run. |
| Repository | Pull requests: write | Review comments, replies, thread resolution and fix pull requests. |
| Repository | Issues: write | The summary comment and reactions. |
| Repository | Metadata: read | Repository data. |
| Organization | Members: read | Organization membership and team roles at sign-in. |

Sayso mints an installation token for each activity. For an agent run, it narrows the token to one repository with `contents: read` and `metadata: read`. The agent removes the token from the git configuration after the clone.

## Events

The App subscribes to these events:

- `pull_request`
- `pull_request_review`
- `pull_request_review_comment`
- `pull_request_review_thread`
- `issue_comment`
- `check_run`

GitHub sends `installation` and `installation_repositories` events to every App. Sayso uses them to update the list of installations and repositories.

| Event | Result |
|---|---|
| `pull_request` opened, reopened, ready for review | Starts a round when the pull request is not a draft and the repository mode is not `off`. |
| `pull_request` synchronize | Starts a round on the new head commit after the debounce time. |
| `pull_request` converted to draft | Pauses the review. |
| `pull_request` closed | Ends the review. |
| `issue_comment` that starts with `/sayso` | Runs a command. |
| `pull_request_review_comment` in a Sayso thread | Starts a reply run. |
| `pull_request_review_comment` by a person outside Sayso threads | Records a steering signal. Sayso does not reply. |
| `pull_request_review_thread` resolved or unresolved | Updates the finding status. |
| `check_run` rerequested on the `sayso` check | Starts a round now. |

The debounce time is the `debounce_seconds` setting (default 90). A command and a check rerun skip it.

## Webhook ingress

Sayso checks the `X-Hub-Signature-256` header with the webhook secret before it parses a delivery. A wrong signature gets 401 and an audit entry. Sayso handles each delivery ID once.

### Direct ingress

GitHub sends webhooks to `POST /webhooks/github` on the API. Use this mode when GitHub can reach the API.

```sh
SAYSO_INGRESS=direct
SAYSO_BASE_URL=https://sayso.example.test
```

### Relay ingress

Use the relay when the API has no public ingress. The relay is the `relay` role of the same binary, on a public host.

1. GitHub sends webhooks to `POST /webhooks/github` on the relay.
2. The API opens `GET /relay/v1/stream` on the relay as an outbound connection, with the bearer `SAYSO_RELAY_TOKEN`. The API gives the relay the webhook secret on this connection.
3. The relay checks each signature, keeps up to 10,000 events or 24 hours in memory, and streams them to the API.
4. The API acknowledges each delivery. The relay then forgets it.

The relay stores nothing on disk and has no database access. Until the API connects, the relay cannot check signatures, so it refuses deliveries. GitHub records them as failed, and the reconciler asks GitHub to deliver them again.

On the API and worker:

```sh
SAYSO_INGRESS=relay
SAYSO_RELAY_URL=https://relay.example.test
SAYSO_RELAY_TOKEN=<openssl rand -hex 32>
SAYSO_PUBLIC_WEBHOOK_URL=https://relay.example.test/webhooks/github
```

On the relay:

```sh
SAYSO_ROLE=relay
SAYSO_RELAY_TOKEN=<the same token>
```

In Compose, start the relay with `docker compose --profile relay up -d`, and set `SAYSO_RELAY_URL=http://relay:8080`. In Helm, set `relay.enabled=true`, `ingressMode=relay` and `publicWebhookURL`. Use `relay.ingress.*` to expose only `/webhooks/github` of the relay.

Set `SAYSO_PUBLIC_WEBHOOK_URL` before you create the App. The manifest uses it as the webhook URL.

### Reconciler

The reconciler always runs, in both modes. Every `reconcile_interval_minutes` (default 10), it does these steps:

1. It lists the App webhook deliveries of the last 24 hours. It asks GitHub to deliver each failed delivery again when Sayso did not receive it.
2. For each repository with a mode other than `off`, it lists the open pull requests that are not drafts. It starts a round for each head commit that has no round.

## Sign-in

People sign in with GitHub through the user authorization of the App. Sayso accepts only members of the App organization.

Sessions use HttpOnly and SameSite=Lax cookies. The cookies are Secure when `SAYSO_BASE_URL` starts with `https://`. A session ends after 12 hours without use, or after 7 days.

## Roles

| Role | Can |
|---|---|
| `admin` | Do all actions, including settings, access, audit and diagnostics. |
| `maintainer` | Edit repositories, use the steering queue, cancel and rerun rounds, and see usage. |
| `viewer` | Read reviews, rounds and runs. |

A person gets roles from these sources. The highest role wins.

- **Team mappings.** On Settings → Access, map a team slug to a role. Sayso checks the teams at each sign-in and every 15 minutes.
- **Grants.** Give a role to a login on Settings → Access, or with the CLI:

  ```sh
  sayso admin grant --github-login dev-alice --role maintainer
  ```

- **Bootstrap admins.** Each login in `SAYSO_BOOTSTRAP_ADMINS` gets `admin` at first sign-in.

An organization member with no grant and no mapped team is a `viewer`. A person who leaves the organization loses access at the next check.

The `/sayso` commands use the repository permission of the commenter on GitHub, not the Sayso role. See the command table in the [README](../README.md#commands).
