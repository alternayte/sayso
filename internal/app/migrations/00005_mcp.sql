-- +goose Up
CREATE TABLE mcp_servers (
    name          text PRIMARY KEY,
    url           text NOT NULL,
    headers       jsonb NOT NULL DEFAULT '{}',
    secret_header text NOT NULL DEFAULT '',
    secret_id     uuid REFERENCES secrets (id),
    allow_tools   text[] NOT NULL DEFAULT '{}',
    allow_write   boolean NOT NULL DEFAULT false,
    tools_cache   jsonb,
    checked_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE mcp_servers;
