-- +goose Up
CREATE TABLE installations (
    installation_id bigint PRIMARY KEY,
    account_login   text NOT NULL,
    suspended_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE repositories (
    id                     uuid PRIMARY KEY,
    github_id              bigint NOT NULL UNIQUE,
    installation_id        bigint NOT NULL REFERENCES installations (installation_id),
    full_name              text NOT NULL,
    default_branch         text NOT NULL DEFAULT 'main',
    private                boolean NOT NULL DEFAULT true,
    mode                   text NOT NULL DEFAULT 'off' CHECK (mode IN ('off', 'shadow', 'comment', 'gate')),
    profile_review         text NOT NULL DEFAULT 'review',
    profile_challenge      text NOT NULL DEFAULT 'challenge',
    profile_reply          text NOT NULL DEFAULT 'reply',
    profile_fix            text NOT NULL DEFAULT 'fix',
    skael_set              text NOT NULL DEFAULT '',
    agent_image            text NOT NULL DEFAULT '',
    mcp_servers            text[] NOT NULL DEFAULT '{}',
    feeds                  text[] NOT NULL DEFAULT '{}',
    max_concurrent_rounds  integer,
    execution              boolean NOT NULL DEFAULT true,
    challenge_min_severity text NOT NULL DEFAULT 'minor' CHECK (challenge_min_severity IN ('blocker', 'major', 'minor', 'nit')),
    enabled_at             timestamptz,
    removed_at             timestamptz,
    created_at             timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX repositories_full_name ON repositories (lower(full_name)) WHERE removed_at IS NULL;

CREATE TABLE webhook_deliveries (
    delivery_id  text PRIMARY KEY,
    event        text NOT NULL,
    action       text NOT NULL DEFAULT '',
    received_via text NOT NULL CHECK (received_via IN ('direct', 'relay', 'redelivery')),
    received_at  timestamptz NOT NULL DEFAULT now(),
    handled_at   timestamptz,
    error        text NOT NULL DEFAULT ''
);
CREATE INDEX webhook_deliveries_received_at ON webhook_deliveries (received_at DESC);

-- Deliveries that the reconciler asked GitHub to send again. A delivery in
-- this table is stored with received_via redelivery.
CREATE TABLE webhook_redeliveries (
    delivery_id  text PRIMARY KEY,
    requested_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE reviews (
    id                 uuid PRIMARY KEY,
    repository_id      uuid NOT NULL REFERENCES repositories (id),
    pr_number          integer NOT NULL,
    pr_node_id         text NOT NULL DEFAULT '',
    author_login       text NOT NULL DEFAULT '',
    state              text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'paused', 'closed', 'merged')),
    workflow_id        text NOT NULL,
    summary_comment_id bigint,
    head_sha           text NOT NULL DEFAULT '',
    base_sha           text NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now(),
    closed_at          timestamptz,
    UNIQUE (repository_id, pr_number)
);

CREATE TABLE rounds (
    id              uuid PRIMARY KEY,
    review_id       uuid NOT NULL REFERENCES reviews (id),
    head_sha        text NOT NULL,
    base_sha        text NOT NULL,
    trigger         text NOT NULL CHECK (trigger IN ('opened', 'ready', 'push', 'command', 'check_rerequest', 'reconcile')),
    status          text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'publishing', 'done', 'failed', 'cancelled', 'budget_exceeded')),
    verdict         text CHECK (verdict IN ('blocking', 'changes_suggested', 'looks_good')),
    risk            text CHECK (risk IN ('low', 'medium', 'high', 'critical')),
    assessment      jsonb,
    check_run_id    bigint,
    override        jsonb,
    config_snapshot jsonb,
    skill_ref       jsonb,
    prompt_ref      jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz
);
CREATE INDEX rounds_review_head ON rounds (review_id, head_sha);

-- +goose Down
DROP TABLE rounds;
DROP TABLE reviews;
DROP TABLE webhook_redeliveries;
DROP TABLE webhook_deliveries;
DROP TABLE repositories;
DROP TABLE installations;
