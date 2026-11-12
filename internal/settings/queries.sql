-- name: SeedDefault :exec
INSERT INTO settings (key, value, revision, updated_by)
VALUES ($1, $2, 1, 'system')
ON CONFLICT (key) DO NOTHING;

-- name: SeedHistory :exec
INSERT INTO settings_history (key, revision, old_value, new_value, updated_by)
SELECT key, 1, NULL, value, 'system' FROM settings WHERE settings.key = $1 AND revision = 1
ON CONFLICT (key, revision) DO NOTHING;

-- name: Get :one
SELECT * FROM settings WHERE key = $1;

-- name: GetForUpdate :one
SELECT * FROM settings WHERE key = $1 FOR UPDATE;

-- name: All :many
SELECT * FROM settings ORDER BY key;

-- name: Update :one
UPDATE settings SET value = $2, revision = revision + 1, updated_by = $3, updated_at = now()
WHERE key = $1
RETURNING *;

-- name: InsertHistory :exec
INSERT INTO settings_history (key, revision, old_value, new_value, updated_by, updated_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: History :many
SELECT * FROM settings_history WHERE key = $1 ORDER BY revision;
