-- name: List :many
SELECT * FROM package_feeds ORDER BY name;

-- name: Get :one
SELECT * FROM package_feeds WHERE name = $1;

-- name: Upsert :one
INSERT INTO package_feeds (name, kind, upstream_url, auth_style, auth_header, username, secret_id, mode, match_urls, repos,
    allowed_repositories, inject_confirmed_by, cache_enabled, cache_max_bytes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (name) DO UPDATE SET kind = EXCLUDED.kind, upstream_url = EXCLUDED.upstream_url, auth_style = EXCLUDED.auth_style,
    auth_header = EXCLUDED.auth_header, username = EXCLUDED.username, secret_id = EXCLUDED.secret_id, mode = EXCLUDED.mode,
    match_urls = EXCLUDED.match_urls, repos = EXCLUDED.repos, allowed_repositories = EXCLUDED.allowed_repositories,
    inject_confirmed_by = EXCLUDED.inject_confirmed_by, cache_enabled = EXCLUDED.cache_enabled,
    cache_max_bytes = EXCLUDED.cache_max_bytes, updated_at = now()
RETURNING *;

-- name: DeleteFeed :exec
DELETE FROM package_feeds WHERE name = $1;

-- name: SetChecked :exec
UPDATE package_feeds SET checked_at = now() WHERE name = $1;

-- name: CacheGet :one
SELECT * FROM pkg_cache WHERE feed = $1 AND path = $2;

-- name: CachePut :exec
INSERT INTO pkg_cache (feed, path, store_id, key, size, sha256) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (feed, path) DO UPDATE SET store_id = EXCLUDED.store_id, key = EXCLUDED.key, size = EXCLUDED.size,
    sha256 = EXCLUDED.sha256, hits = 0, last_access_at = now(), created_at = now();

-- name: CacheHit :exec
UPDATE pkg_cache SET hits = hits + 1, last_access_at = now() WHERE feed = $1 AND path = $2;

-- name: CacheDelete :exec
DELETE FROM pkg_cache WHERE feed = $1 AND path = $2;

-- name: CacheSize :one
SELECT COALESCE(sum(size), 0)::bigint FROM pkg_cache WHERE feed = $1;

-- name: CacheOldest :many
SELECT * FROM pkg_cache WHERE feed = $1 ORDER BY last_access_at, created_at LIMIT $2;

-- name: AddUsage :exec
INSERT INTO pkg_usage (run_id, feed, requests, bytes, cache_hits) VALUES ($1, $2, 1, $3, $4)
ON CONFLICT (run_id, feed) DO UPDATE SET requests = pkg_usage.requests + 1, bytes = pkg_usage.bytes + EXCLUDED.bytes,
    cache_hits = pkg_usage.cache_hits + EXCLUDED.cache_hits;

-- name: RunUsage :many
SELECT * FROM pkg_usage WHERE run_id = $1 ORDER BY feed;

-- name: RepositoryFullName :one
SELECT full_name FROM repositories WHERE id = $1;
