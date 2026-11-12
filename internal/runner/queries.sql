-- name: CreateRun :one
INSERT INTO agent_runs (id, round_id, review_id, repository_id, kind, runner, status, token_hash, token_expires_at, models, budget_usd, stages, spec, started_at)
VALUES ($1, $2, $3, $4, $5, $6, 'running', $7, $8, $9, $10, $11, $12, now())
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

-- name: Heartbeat :one
UPDATE agent_runs SET heartbeat_at = now() WHERE id = $1 RETURNING cancel_requested;

-- name: RequestCancel :exec
UPDATE agent_runs SET cancel_requested = true WHERE id = $1;

-- name: PutArtifact :exec
INSERT INTO artifacts (run_id, name, store_id, key, size, sha256, content_type)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (run_id, name) DO UPDATE SET store_id = EXCLUDED.store_id, key = EXCLUDED.key, size = EXCLUDED.size,
    sha256 = EXCLUDED.sha256, content_type = EXCLUDED.content_type, created_at = now();

-- name: Artifacts :many
SELECT * FROM artifacts WHERE run_id = $1 ORDER BY name;

-- name: SetExternalRef :exec
UPDATE agent_runs SET external_ref = $2, harness_version = CASE WHEN @harness::text = '' THEN harness_version ELSE @harness::text END WHERE id = $1;

-- name: SetHarness :exec
UPDATE agent_runs SET harness_version = $2 WHERE id = $1;
