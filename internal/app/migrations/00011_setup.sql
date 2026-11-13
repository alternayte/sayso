-- +goose Up
-- The last store test, for the setup checklist (§16.3).
ALTER TABLE stores ADD COLUMN tested_at timestamptz;
ALTER TABLE stores ADD COLUMN test_ok boolean NOT NULL DEFAULT false;

-- One row when every checklist step was done once. The home page then
-- stays the reviews list.
CREATE TABLE setup_completed (
    id           boolean PRIMARY KEY DEFAULT true CHECK (id),
    completed_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE setup_completed;
ALTER TABLE stores DROP COLUMN test_ok;
ALTER TABLE stores DROP COLUMN tested_at;
