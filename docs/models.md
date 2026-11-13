# Models

Sayso calls models only through providers that an admin configures. The agent reaches them only through the model proxy in the API. Configure providers and profiles on Settings → Models.

## Providers

A provider is one model endpoint.

| Field | Use |
|---|---|
| `name` | Lower-case letters, digits and hyphens. The proxy route uses it. |
| `api_type` | `openai-completions`, `openai-responses`, `anthropic-messages` or `google-generative-ai`. |
| `base_url` | Base URL of the API, for example `https://api.openai.com/v1`. |
| `auth_style` | How the proxy sends the key: `bearer` (`Authorization: Bearer`), `x_api_key` (`x-api-key`), `api_key` (`api-key`) or `header`. |
| `auth_header` | Header name for `auth_style: header`. |
| key | The provider key. Type a value, or give the name of an environment variable. |
| `headers` | Extra headers as JSON. Do not put a key in them. Sayso refuses `Authorization`, `X-Api-Key`, `Api-Key` and `X-Goog-Api-Key` here. |
| `compat` | Compatibility flags as JSON. The agent runtime gets them. |
| `models` | Model IDs for the pickers. |
| `max_concurrency` | Requests in flight at one time. 0 means no limit. |
| `tokens_per_minute` | Token limit for each minute. 0 means no limit. |

### Presets

A preset fills the base URL, the API type and the authentication style. All fields stay editable.

| Preset | API type | Base URL | Auth style |
|---|---|---|---|
| OpenAI | `openai-responses` | `https://api.openai.com/v1` | `bearer` |
| Anthropic | `anthropic-messages` | `https://api.anthropic.com/v1` | `x_api_key` |
| Azure AI Foundry | `openai-completions` | `https://RESOURCE.openai.azure.com/openai/v1` | `api_key` |
| OpenRouter | `openai-completions` | `https://openrouter.ai/api/v1` | `bearer` |
| DeepSeek | `openai-completions` | `https://api.deepseek.com/v1` | `bearer` |
| Cloudflare AI Gateway | `openai-completions` | `https://gateway.ai.cloudflare.com/v1/ACCOUNT/GATEWAY/compat` | `bearer` |
| OpenAI-compatible gateway | `openai-completions` | your gateway URL, up to and including `/v1` | `bearer` |

Replace `RESOURCE`, `ACCOUNT` and `GATEWAY` with your values.

### Test a provider

Click **Test** on the provider. The smoke test makes three calls:

1. One plain call.
2. One tool call round trip.
3. One structured JSON answer.

Sayso stores the result of each check, the latency and the model ID of the response. A profile cannot use a provider that did not pass the tool call check.

## Profiles

A profile is a named model choice for a stage. It has an ordered list of targets and a round budget.

Sayso has four default profiles. Each one serves one stage:

| Profile | Stage |
|---|---|
| `review` | Reads the change and records findings. |
| `challenge` | Tries to refute each candidate finding. Use a different model family from `review` for a better check. |
| `reply` | Answers a person in a Sayso thread, and runs `/sayso explain`. |
| `fix` | Writes a patch for `/sayso fix`. |

Give each default profile one or more targets before the first round.

A target has these fields:

| Field | Use |
|---|---|
| `provider` | Provider name. |
| `model` | Model ID. |
| `temperature` | Optional. |
| `max_tokens` | Maximum output tokens. |
| `reasoning` | `off`, `low`, `medium` or `high`. |
| `context_window` | Context window in tokens. |
| `price_in`, `price_out`, `price_cache_read` | USD for each million input, output and cache-read tokens. Sayso computes the cost from these prices. |

A repository can use other profiles for each stage. The profile must be in the `allowed_profiles` setting. Set the profiles on the repository page, or in `.sayso.yml`:

```yaml
profiles:
  review: review-strong
```

### Fallback

The targets of a profile are in fallback order. When a target returns 429 or 5xx after the proxy retries, or the connection fails, the agent runtime moves to the next target.

### Challenge threshold

Each repository has a challenge threshold: `blocker`, `major`, `minor` or `nit`. The default is `minor`. The challenge stage gets every new candidate at or above the threshold, in one session. It runs only when one or more such candidates exist.

| Outcome | Result |
|---|---|
| Confirmed | The finding is posted. A confirmed `blocker` with `test`, `trace` or `call_path` evidence can block. |
| Refuted | The finding becomes `withdrawn` and is not posted. |
| Downgraded | The finding drops one severity level. A downgraded `nit` becomes `withdrawn`. |

Set the threshold on the repository page. `.sayso.yml` cannot change it. A lower threshold costs more model calls.

## Model proxy

The agent runtime calls `${SAYSO_RUNNER_API_URL}/llm/<provider>/<path>`. It uses its run token as the API key.

For each request, the proxy does these steps:

1. It accepts only an active run token.
2. It allows only the providers and models of the profiles of that run.
3. It refuses a request that would pass the round budget.
4. It waits for provider capacity for up to 60 seconds. Then it returns 429.
5. It removes the incoming credential and adds the provider key in the auth style of the provider. It adds the extra headers.
6. It forwards the path, query and body to the base URL, and streams the response back unchanged.
7. It retries 429, 5xx and connection errors up to 2 times with backoff, only before the first response byte.
8. It reads the token usage, computes the cost and records a `model_turn` event.

## Budgets

| Budget | Where | Result |
|---|---|---|
| Round budget | `round_budget_usd` on each profile. A run uses the highest budget of its profiles. 0 means no limit. | The proxy refuses a request that would pass it. The round ends as `budget_exceeded`, and the check is `neutral`. |
| Monthly budget | The `monthly_budget_usd` setting on Settings → General. 0 means no limit. | No new round starts when the model cost of this month reaches it. The UI shows a banner. |

The proxy estimates the cost of a request from its input size and its maximum output tokens. The Usage page shows tokens and cost.

## Fair scheduling

An admission queue in PostgreSQL decides which agent run starts next.

1. Reply runs and fix runs go first, oldest first.
2. Rounds go next, round-robin across repositories. The repository that got a slot least recently goes first. Inside a repository, the oldest round goes first.
3. A repository has at most `max_concurrent_rounds` rounds running at one time. The default is 2. A repository can have its own value.

| Setting | Default | Use |
|---|---|---|
| `worker_max_agent_runs` | 4 | Agent runs that run at the same time. The admission queue applies it to all workers together. Each worker also runs at most this number. |
| `max_concurrent_rounds` | 2 | Rounds that run at the same time in one repository. |
| `round_timeout_minutes` | 30 | Time limit of a round agent run. |
| `reply_timeout_minutes` | 5 | Time limit of a reply run. |

A waiting run that stops its heartbeat for one minute loses its place. A slot is released when the run ends.

Provider limits apply in the proxy. `max_concurrency` limits requests in flight. `tokens_per_minute` limits tokens for each minute.
