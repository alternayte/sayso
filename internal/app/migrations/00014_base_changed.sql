-- +goose Up
-- A pull request that moves to a new base branch gets a round when its
-- merge base changes.
ALTER TABLE rounds DROP CONSTRAINT rounds_trigger_check;
ALTER TABLE rounds ADD CONSTRAINT rounds_trigger_check
    CHECK (trigger IN ('opened', 'ready', 'push', 'command', 'check_rerequest', 'reconcile', 'base_changed'));

-- +goose Down
UPDATE rounds SET trigger = 'push' WHERE trigger = 'base_changed';
ALTER TABLE rounds DROP CONSTRAINT rounds_trigger_check;
ALTER TABLE rounds ADD CONSTRAINT rounds_trigger_check
    CHECK (trigger IN ('opened', 'ready', 'push', 'command', 'check_rerequest', 'reconcile'));
