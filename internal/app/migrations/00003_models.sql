-- +goose Up
CREATE TABLE providers (
    name              text PRIMARY KEY,
    api_type          text NOT NULL CHECK (api_type IN ('openai-completions', 'openai-responses', 'anthropic-messages', 'google-generative-ai')),
    base_url          text NOT NULL,
    auth_style        text NOT NULL CHECK (auth_style IN ('bearer', 'x_api_key', 'api_key', 'header')),
    auth_header       text NOT NULL DEFAULT '',
    secret_id         uuid REFERENCES secrets (id),
    headers           jsonb NOT NULL DEFAULT '{}',
    compat            jsonb NOT NULL DEFAULT '{}',
    models            text[] NOT NULL DEFAULT '{}',
    smoke             jsonb,
    smoke_at          timestamptz,
    max_concurrency   integer NOT NULL DEFAULT 0,
    tokens_per_minute integer NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE profiles (
    name             text PRIMARY KEY,
    targets          jsonb NOT NULL DEFAULT '[]',
    round_budget_usd numeric(12, 4) NOT NULL DEFAULT 0,
    updated_at       timestamptz NOT NULL DEFAULT now()
);
INSERT INTO profiles (name) VALUES ('review'), ('challenge'), ('reply'), ('fix');

CREATE TABLE agent_runs (
    id              uuid PRIMARY KEY,
    round_id        uuid REFERENCES rounds (id),
    review_id       uuid REFERENCES reviews (id),
    repository_id   uuid REFERENCES repositories (id),
    kind            text NOT NULL CHECK (kind IN ('round', 'reply', 'fix', 'noop')),
    runner          text NOT NULL DEFAULT 'process',
    external_ref    text NOT NULL DEFAULT '',
    status          text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed', 'cancelled', 'budget_exceeded')),
    token_hash      bytea UNIQUE,
    token_expires_at timestamptz,
    -- Snapshot of the model targets the run may call, with prices and budgets.
    models          jsonb NOT NULL DEFAULT '[]',
    budget_usd      numeric(12, 4) NOT NULL DEFAULT 0,
    stages          jsonb NOT NULL DEFAULT '[]',
    harness_version text NOT NULL DEFAULT '',
    image           text NOT NULL DEFAULT '',
    exit_reason     text NOT NULL DEFAULT '',
    tokens_in       bigint NOT NULL DEFAULT 0,
    tokens_out      bigint NOT NULL DEFAULT 0,
    cost_usd        numeric(12, 6) NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz
);

CREATE TABLE run_events (
    run_id      uuid NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    seq         bigint NOT NULL,
    at          timestamptz NOT NULL DEFAULT now(),
    stage       text NOT NULL DEFAULT '',
    type        text NOT NULL CHECK (type IN ('model_turn', 'tool_call', 'mcp_call', 'skill_load', 'finding', 'env', 'pkg_summary', 'note', 'error')),
    name        text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT '',
    duration_ms integer NOT NULL DEFAULT 0,
    tokens_in   bigint NOT NULL DEFAULT 0,
    tokens_out  bigint NOT NULL DEFAULT 0,
    preview     text NOT NULL DEFAULT '' CHECK (char_length(preview) <= 500),
    payload_key text,
    PRIMARY KEY (run_id, seq)
);
-- Server-side events (model proxy, MCP proxy) take numbers from this sequence,
-- above the range that the runtime uses.
CREATE SEQUENCE run_events_server_seq START 1000000000;

CREATE TABLE model_usage (
    id                bigserial PRIMARY KEY,
    run_id            uuid NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    provider          text NOT NULL,
    model             text NOT NULL,
    tokens_in         bigint NOT NULL DEFAULT 0,
    tokens_out        bigint NOT NULL DEFAULT 0,
    tokens_cache_read bigint NOT NULL DEFAULT 0,
    cost_usd          numeric(12, 6) NOT NULL DEFAULT 0,
    status            integer NOT NULL,
    latency_ms        integer NOT NULL DEFAULT 0,
    at                timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX model_usage_run ON model_usage (run_id);
CREATE INDEX model_usage_at ON model_usage (at);

-- +goose Down
DROP TABLE model_usage;
DROP SEQUENCE run_events_server_seq;
DROP TABLE run_events;
DROP TABLE agent_runs;
DROP TABLE profiles;
DROP TABLE providers;
