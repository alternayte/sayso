-- name: KeyVersionByFingerprint :one
SELECT version FROM secret_keys WHERE fingerprint = $1;

-- name: MaxKeyVersion :one
SELECT COALESCE(max(version), 0)::integer FROM secret_keys;

-- name: InsertKey :exec
INSERT INTO secret_keys (version, fingerprint) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: Get :one
SELECT * FROM secrets WHERE name = $1;

-- name: GetByID :one
SELECT * FROM secrets WHERE id = $1;

-- name: All :many
SELECT * FROM secrets ORDER BY name;

-- name: UpsertStored :one
INSERT INTO secrets (id, name, kind, ciphertext, nonce, key_version, env_name, last4, updated_by)
VALUES ($1, $2, 'stored', $3, $4, $5, NULL, $6, $7)
ON CONFLICT (name) DO UPDATE SET kind = 'stored', ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce,
    key_version = EXCLUDED.key_version, env_name = NULL, last4 = EXCLUDED.last4,
    updated_by = EXCLUDED.updated_by, updated_at = now()
RETURNING *;

-- name: UpsertEnvRef :one
INSERT INTO secrets (id, name, kind, ciphertext, nonce, key_version, env_name, last4, updated_by)
VALUES ($1, $2, 'env_ref', NULL, NULL, NULL, $3, '', $4)
ON CONFLICT (name) DO UPDATE SET kind = 'env_ref', ciphertext = NULL, nonce = NULL, key_version = NULL,
    env_name = EXCLUDED.env_name, last4 = '', updated_by = EXCLUDED.updated_by, updated_at = now()
RETURNING *;

-- name: StoredForUpdate :many
SELECT * FROM secrets WHERE kind = 'stored' ORDER BY name FOR UPDATE;

-- name: Reseal :exec
UPDATE secrets SET ciphertext = $2, nonce = $3, key_version = $4 WHERE id = $1;

-- name: Delete :exec
DELETE FROM secrets WHERE name = $1;
