-- +goose Up
CREATE TABLE package_feeds (
    name                 text PRIMARY KEY,
    kind                 text NOT NULL CHECK (kind IN ('npm', 'nuget', 'pypi', 'maven', 'goproxy', 'github')),
    upstream_url         text NOT NULL DEFAULT '',
    auth_style           text NOT NULL DEFAULT 'bearer' CHECK (auth_style IN ('basic', 'bearer', 'header')),
    auth_header          text NOT NULL DEFAULT '',
    username             text NOT NULL DEFAULT '',
    secret_id            uuid REFERENCES secrets (id),
    mode                 text NOT NULL DEFAULT 'proxy' CHECK (mode IN ('proxy', 'inject')),
    match_urls           text[] NOT NULL DEFAULT '{}',
    repos                text[] NOT NULL DEFAULT '{}',
    allowed_repositories text[],
    inject_confirmed_by  text NOT NULL DEFAULT '',
    cache_enabled        boolean NOT NULL DEFAULT true,
    cache_max_bytes      bigint NOT NULL DEFAULT 5368709120,
    checked_at           timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE pkg_cache (
    feed           text NOT NULL REFERENCES package_feeds (name) ON DELETE CASCADE,
    path           text NOT NULL,
    store_id       uuid NOT NULL REFERENCES stores (id),
    key            text NOT NULL,
    size           bigint NOT NULL,
    sha256         text NOT NULL,
    hits           bigint NOT NULL DEFAULT 0,
    last_access_at timestamptz NOT NULL DEFAULT now(),
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (feed, path)
);
CREATE INDEX pkg_cache_lru ON pkg_cache (feed, last_access_at);

-- Requests and bytes per run and feed, for the pkg_summary event.
CREATE TABLE pkg_usage (
    run_id     uuid NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    feed       text NOT NULL,
    requests   bigint NOT NULL DEFAULT 0,
    bytes      bigint NOT NULL DEFAULT 0,
    cache_hits bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, feed)
);

CREATE TABLE extensions (
    name         text PRIMARY KEY,
    kind         text NOT NULL DEFAULT 'pi_package' CHECK (kind IN ('pi_package', 'skael_hook')),
    spec         text NOT NULL,
    env          jsonb NOT NULL DEFAULT '{}',
    secret_ids   jsonb NOT NULL DEFAULT '{}',
    enabled      boolean NOT NULL DEFAULT false,
    repositories text[] NOT NULL DEFAULT '{}',
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE agent_images (
    name       text PRIMARY KEY,
    image      text NOT NULL,
    is_default boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX agent_images_one_default ON agent_images (is_default) WHERE is_default;

CREATE TABLE steering_signals (
    id          uuid PRIMARY KEY,
    review_id   uuid REFERENCES reviews (id),
    round_id    uuid REFERENCES rounds (id),
    finding_id  uuid,
    type        text NOT NULL CHECK (type IN ('dismissed', 'overridden', 'conceded', 'resolved_unfixed', 'human_comment_unflagged', 'rerun_requested')),
    actor_login text NOT NULL DEFAULT '',
    detail      jsonb NOT NULL DEFAULT '{}',
    at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX steering_signals_round ON steering_signals (round_id);

-- Skael reports that failed after retries, for the Diagnostics page.
CREATE TABLE skael_reports (
    round_id   uuid PRIMARY KEY REFERENCES rounds (id),
    attempts   integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    reported_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE skael_reports;
DROP TABLE steering_signals;
DROP TABLE agent_images;
DROP TABLE extensions;
DROP TABLE pkg_usage;
DROP TABLE pkg_cache;
DROP TABLE package_feeds;
