-- name: CreateRun :one
INSERT INTO agent_runs (id, round_id, review_id, repository_id, kind, runner, status, token_hash, token_expires_at, models, budget_usd, stages, started_at)
VALUES ($1, $2, $3, $4, $5, $6, 'running', $7, $8, $9, $10, $11, now())
RETURNING *;

-- name: RunByTokenHash :one
SELECT * FROM agent_runs WHERE token_hash = $1;

-- name: GetRun :one
SELECT * FROM agent_runs WHERE id = $1;

-- name: FinishRun :one
UPDATE agent_runs SET status = @status, exit_reason = @exit_reason, finished_at = now(), token_expires_at = LEAST(token_expires_at, now())
WHERE id = @id
RETURNING *;

-- name: InsertServerEvent :exec
INSERT INTO run_events (run_id, seq, stage, type, name, status, duration_ms, tokens_in, tokens_out, preview)
VALUES ($1, nextval('run_events_server_seq'), $2, $3, $4, $5, $6, $7, $8, $9);
