-- +goose Up
-- A pre-existing finding is a bug at the base commit that the pull request
-- did not introduce. It never blocks.
ALTER TABLE findings ADD COLUMN pre_existing boolean NOT NULL DEFAULT false;

-- The findings of the review stage of a failed run, not challenged. They
-- show in the preview only and never enter the ledger.
ALTER TABLE rounds ADD COLUMN unchallenged jsonb;

-- A command that failed after its retries.
CREATE TABLE failed_commands (
    id          uuid PRIMARY KEY,
    review_id   uuid NOT NULL REFERENCES reviews (id),
    comment_id  bigint NOT NULL,
    source      text NOT NULL CHECK (source IN ('issue', 'review')),
    in_reply_to bigint NOT NULL DEFAULT 0,
    actor_login text NOT NULL DEFAULT '',
    command     text NOT NULL DEFAULT '',
    reason      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    retried_at  timestamptz,
    UNIQUE (review_id, comment_id)
);

-- A batch fix names several findings; finding_id is the first of them.
ALTER TABLE agent_work ADD COLUMN finding_ids uuid[] NOT NULL DEFAULT '{}';

-- The pull request title and head branch, for the reviews list.
ALTER TABLE reviews ADD COLUMN title text NOT NULL DEFAULT '';
ALTER TABLE reviews ADD COLUMN head_ref text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE reviews DROP COLUMN head_ref;
ALTER TABLE reviews DROP COLUMN title;
ALTER TABLE agent_work DROP COLUMN finding_ids;
DROP TABLE failed_commands;
ALTER TABLE rounds DROP COLUMN unchallenged;
ALTER TABLE findings DROP COLUMN pre_existing;
