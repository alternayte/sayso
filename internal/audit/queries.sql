-- name: Insert :exec
INSERT INTO audit_log (actor_user_id, actor_kind, actor_login, action, target, details)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: List :many
SELECT * FROM audit_log
WHERE (sqlc.narg('action')::text IS NULL OR action = sqlc.narg('action'))
  AND (sqlc.narg('before_id')::bigint IS NULL OR id < sqlc.narg('before_id'))
ORDER BY id DESC
LIMIT $1;

-- name: Actions :many
SELECT DISTINCT action FROM audit_log ORDER BY action;
