-- name: List :many
SELECT * FROM mcp_servers ORDER BY name;

-- name: Get :one
SELECT * FROM mcp_servers WHERE name = $1;

-- name: Insert :one
INSERT INTO mcp_servers (name, url, headers, secret_header, secret_id, allow_tools, allow_write)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: Update :one
UPDATE mcp_servers SET url = $2, headers = $3, secret_header = $4, secret_id = $5, allow_tools = $6, allow_write = $7, updated_at = now()
WHERE name = $1
RETURNING *;

-- name: DeleteServer :exec
DELETE FROM mcp_servers WHERE name = $1;

-- name: SetTools :exec
UPDATE mcp_servers SET tools_cache = $2, checked_at = now() WHERE name = $1;

-- name: RepositoryServers :one
SELECT mcp_servers FROM repositories WHERE id = $1;
