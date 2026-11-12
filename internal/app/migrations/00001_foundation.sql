-- +goose Up
CREATE TABLE users (
    id            uuid PRIMARY KEY,
    github_id     bigint NOT NULL UNIQUE,
    login         text NOT NULL,
    name          text NOT NULL DEFAULT '',
    role          text NOT NULL CHECK (role IN ('admin', 'maintainer', 'viewer')),
    role_source   text NOT NULL CHECK (role_source IN ('grant', 'team', 'default')),
    status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'removed')),
    last_login_at timestamptz,
    roles_checked_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_login_lower ON users (lower(login));

CREATE TABLE role_grants (
    login      text PRIMARY KEY,
    role       text NOT NULL CHECK (role IN ('admin', 'maintainer', 'viewer')),
    granted_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id           uuid PRIMARY KEY,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    ip           text NOT NULL DEFAULT '',
    user_agent   text NOT NULL DEFAULT ''
);

CREATE TABLE team_roles (
    team_slug text PRIMARY KEY,
    role      text NOT NULL CHECK (role IN ('admin', 'maintainer', 'viewer'))
);

CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    revision   integer NOT NULL,
    updated_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE settings_history (
    id         bigserial PRIMARY KEY,
    key        text NOT NULL,
    revision   integer NOT NULL,
    old_value  jsonb,
    new_value  jsonb NOT NULL,
    updated_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (key, revision)
);

CREATE TABLE secrets (
    id          uuid PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    kind        text NOT NULL CHECK (kind IN ('stored', 'env_ref')),
    ciphertext  bytea,
    nonce       bytea,
    key_version integer,
    env_name    text,
    last4       text NOT NULL DEFAULT '',
    updated_by  text NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind = 'stored' AND ciphertext IS NOT NULL AND nonce IS NOT NULL AND key_version IS NOT NULL)
        OR (kind = 'env_ref' AND env_name IS NOT NULL))
);

CREATE TABLE secret_keys (
    version     integer PRIMARY KEY,
    fingerprint bytea NOT NULL UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE github_app (
    id                uuid PRIMARY KEY,
    app_id            bigint NOT NULL,
    slug              text NOT NULL,
    org_login         text NOT NULL,
    client_id         text NOT NULL,
    client_secret_id  uuid NOT NULL REFERENCES secrets (id),
    private_key_id    uuid NOT NULL REFERENCES secrets (id),
    webhook_secret_id uuid NOT NULL REFERENCES secrets (id),
    html_url          text NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX github_app_single ON github_app ((true));

CREATE TABLE audit_log (
    id            bigserial PRIMARY KEY,
    at            timestamptz NOT NULL DEFAULT now(),
    actor_user_id uuid,
    actor_kind    text NOT NULL CHECK (actor_kind IN ('user', 'system', 'github')),
    actor_login   text NOT NULL DEFAULT '',
    action        text NOT NULL,
    target        text NOT NULL DEFAULT '',
    details       jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_action_at ON audit_log (action, at DESC);
CREATE INDEX audit_log_at ON audit_log (at DESC);

-- +goose StatementBegin
CREATE FUNCTION audit_log_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE OR TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_append_only();

-- +goose Down
DROP TABLE audit_log;
DROP FUNCTION audit_log_append_only;
DROP TABLE github_app;
DROP TABLE secret_keys;
DROP TABLE secrets;
DROP TABLE settings_history;
DROP TABLE settings;
DROP TABLE team_roles;
DROP TABLE sessions;
DROP TABLE role_grants;
DROP TABLE users;
