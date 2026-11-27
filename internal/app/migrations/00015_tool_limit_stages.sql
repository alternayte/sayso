-- +goose Up
-- The stages of a round that reached the tool-call limit and recorded
-- their output in the reminder. The review can be incomplete.
ALTER TABLE rounds ADD COLUMN tool_limit_stages text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE rounds DROP COLUMN tool_limit_stages;
