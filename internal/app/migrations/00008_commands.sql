-- +goose Up
-- agent_work is a reply or fix run that a command or a thread reply queued (§11).
CREATE TABLE agent_work (
    id           uuid PRIMARY KEY,
    review_id    uuid NOT NULL REFERENCES reviews (id),
    kind         text NOT NULL CHECK (kind IN ('reply', 'fix')),
    finding_id   uuid NOT NULL REFERENCES findings (id),
    -- comment is the comment that asked for the work; source is issue or review.
    comment_id   bigint NOT NULL,
    source       text NOT NULL CHECK (source IN ('issue', 'review')),
    action       text NOT NULL DEFAULT '' CHECK (action IN ('', 'clarify')),
    actor_login  text NOT NULL,
    head_sha     text NOT NULL,
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed', 'cancelled')),
    agent_run_id uuid REFERENCES agent_runs (id),
    spec         jsonb,
    outcome      jsonb,
    reason       text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);
CREATE INDEX agent_work_review ON agent_work (review_id, created_at);

ALTER TABLE agent_runs ADD COLUMN work_id uuid REFERENCES agent_work (id);
ALTER TABLE admissions ADD COLUMN work_id uuid REFERENCES agent_work (id);

-- +goose Down
ALTER TABLE admissions DROP COLUMN work_id;
ALTER TABLE agent_runs DROP COLUMN work_id;
DROP TABLE agent_work;
