-- +goose Up
CREATE TABLE steering_reviews (
    id           uuid PRIMARY KEY,
    round_id     uuid NOT NULL UNIQUE REFERENCES rounds (id),
    tags         text[] NOT NULL DEFAULT '{}',
    note         text NOT NULL DEFAULT '',
    author_login text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX run_events_at ON run_events (at);
CREATE INDEX artifacts_created ON artifacts (created_at);

-- +goose Down
DROP INDEX artifacts_created;
DROP INDEX run_events_at;
DROP TABLE steering_reviews;
