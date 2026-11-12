-- +goose Up
CREATE TABLE stores (
    id             uuid PRIMARY KEY,
    name           text NOT NULL UNIQUE,
    driver         text NOT NULL CHECK (driver IN ('postgres', 's3', 'azblob')),
    config         jsonb NOT NULL DEFAULT '{}',
    secret_id      uuid REFERENCES secrets (id),
    is_default     boolean NOT NULL DEFAULT false,
    retention_days integer NOT NULL DEFAULT 90,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX stores_one_default ON stores (is_default) WHERE is_default;
INSERT INTO stores (id, name, driver, is_default) VALUES ('00000000-0000-7000-8000-000000000001', 'postgres', 'postgres', true);

CREATE TABLE blobs (
    key        text PRIMARY KEY,
    data       bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE artifacts (
    id           bigserial PRIMARY KEY,
    run_id       uuid NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    name         text NOT NULL,
    store_id     uuid NOT NULL REFERENCES stores (id),
    key          text NOT NULL,
    size         bigint NOT NULL,
    sha256       text NOT NULL,
    content_type text NOT NULL DEFAULT 'application/octet-stream',
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, name)
);

ALTER TABLE run_events ADD COLUMN store_id uuid REFERENCES stores (id);
-- The run spec without credentials. The API adds the clone token and the
-- proxy entries when the runtime asks for it.
ALTER TABLE agent_runs ADD COLUMN spec jsonb NOT NULL DEFAULT '{}';
ALTER TABLE agent_runs ADD COLUMN cancel_requested boolean NOT NULL DEFAULT false;
ALTER TABLE agent_runs ADD COLUMN heartbeat_at timestamptz;

-- +goose Down
ALTER TABLE agent_runs DROP COLUMN heartbeat_at;
ALTER TABLE agent_runs DROP COLUMN cancel_requested;
ALTER TABLE agent_runs DROP COLUMN spec;
ALTER TABLE run_events DROP COLUMN store_id;
DROP TABLE artifacts;
DROP TABLE blobs;
DROP TABLE stores;
