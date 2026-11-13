-- name: PutBlob :exec
INSERT INTO blobs (key, data) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET data = EXCLUDED.data, created_at = now();

-- name: GetBlob :one
SELECT data FROM blobs WHERE key = $1;

-- name: StatBlob :one
SELECT octet_length(data)::bigint AS size, created_at FROM blobs WHERE key = $1;

-- name: DeleteBlob :exec
DELETE FROM blobs WHERE key = $1;

-- name: ListBlobs :many
SELECT key FROM blobs WHERE key LIKE @prefix::text || '%' ORDER BY key;

-- name: DefaultStore :one
SELECT * FROM stores WHERE is_default;

-- name: StoreByID :one
SELECT * FROM stores WHERE id = $1;

-- name: ListStores :many
SELECT * FROM stores ORDER BY is_default DESC, name;

-- name: StoreByName :one
SELECT * FROM stores WHERE name = $1;
