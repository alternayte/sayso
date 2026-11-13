-- +goose Up
-- guidance holds the prompt guidance layers (§8.9); the newest version per scope is active.
CREATE TABLE guidance (
    id            uuid PRIMARY KEY,
    scope         text NOT NULL CHECK (scope IN ('global', 'repository')),
    repository_id uuid REFERENCES repositories (id),
    version       integer NOT NULL,
    text          text NOT NULL CHECK (char_length(text) <= 8000),
    author_login  text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((scope = 'global') = (repository_id IS NULL))
);
CREATE UNIQUE INDEX guidance_version ON guidance (scope, coalesce(repository_id, '00000000-0000-0000-0000-000000000000'), version);

-- +goose Down
DROP TABLE guidance;
