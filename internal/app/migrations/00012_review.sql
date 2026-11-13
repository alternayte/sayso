-- +goose Up
-- True after Sayso created the fix branch of the work; Sayso commits only
-- to a branch it created (SI-07).
ALTER TABLE agent_work ADD COLUMN branch_created boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE agent_work DROP COLUMN branch_created;
