-- name: ListProviders :many
SELECT * FROM providers ORDER BY name;

-- name: GetProvider :one
SELECT * FROM providers WHERE name = $1;

-- name: InsertProvider :one
INSERT INTO providers (name, api_type, base_url, auth_style, auth_header, secret_id, headers, compat, models, max_concurrency, tokens_per_minute)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: UpdateProvider :one
UPDATE providers SET api_type = $2, base_url = $3, auth_style = $4, auth_header = $5, secret_id = $6, headers = $7,
    compat = $8, models = $9, max_concurrency = $10, tokens_per_minute = $11, updated_at = now()
WHERE name = $1
RETURNING *;

-- name: DeleteProvider :exec
DELETE FROM providers WHERE name = $1;

-- name: SetSmoke :exec
UPDATE providers SET smoke = $2, smoke_at = now() WHERE name = $1;

-- name: ListProfiles :many
SELECT * FROM profiles ORDER BY name;

-- name: GetProfile :one
SELECT * FROM profiles WHERE name = $1;

-- name: PutProfile :one
INSERT INTO profiles (name, targets, round_budget_usd) VALUES ($1, $2, $3)
ON CONFLICT (name) DO UPDATE SET targets = EXCLUDED.targets, round_budget_usd = EXCLUDED.round_budget_usd, updated_at = now()
RETURNING *;

-- name: DeleteProfile :exec
DELETE FROM profiles WHERE name = $1;

-- name: ProfilesUsingProvider :many
SELECT name FROM profiles WHERE targets @> jsonb_build_array(jsonb_build_object('provider', @provider::text));

-- name: InsertUsage :exec
INSERT INTO model_usage (run_id, provider, model, tokens_in, tokens_out, tokens_cache_read, cost_usd, status, latency_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: RunSpend :one
SELECT COALESCE(sum(cost_usd), 0)::float8 FROM model_usage WHERE run_id = $1;

-- name: AddRunUsage :exec
UPDATE agent_runs SET tokens_in = tokens_in + $2, tokens_out = tokens_out + $3, cost_usd = cost_usd + $4 WHERE id = $1;
