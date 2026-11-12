-- name: UpsertUser :one
INSERT INTO users (id, github_id, login, name, role, role_source, status, last_login_at, roles_checked_at)
VALUES ($1, $2, $3, $4, $5, $6, 'active', $7, $7)
ON CONFLICT (github_id) DO UPDATE SET login = EXCLUDED.login, name = EXCLUDED.name, role = EXCLUDED.role,
    role_source = EXCLUDED.role_source, status = 'active', last_login_at = EXCLUDED.last_login_at,
    roles_checked_at = EXCLUDED.roles_checked_at
RETURNING *;

-- name: UserByID :one
SELECT * FROM users WHERE id = $1;

-- name: UserByLogin :one
SELECT * FROM users WHERE lower(login) = lower(@login::text);

-- name: Users :many
SELECT * FROM users ORDER BY lower(login);

-- name: UpdateUserRole :one
UPDATE users SET role = $2, role_source = $3, status = $4, roles_checked_at = $5 WHERE id = $1 RETURNING *;

-- name: InsertSession :exec
INSERT INTO sessions (id, user_id, token_hash, created_at, last_seen_at, expires_at, ip, user_agent)
VALUES ($1, $2, $3, $4, $4, $5, $6, $7);

-- name: SessionByHash :one
SELECT * FROM sessions WHERE token_hash = $1;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = $2 WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = $1;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at < $1 OR last_seen_at < $2;

-- name: TeamRoles :many
SELECT * FROM team_roles ORDER BY team_slug;

-- name: PutTeamRole :exec
INSERT INTO team_roles (team_slug, role) VALUES ($1, $2)
ON CONFLICT (team_slug) DO UPDATE SET role = EXCLUDED.role;

-- name: DeleteTeamRole :exec
DELETE FROM team_roles WHERE team_slug = $1;

-- name: GrantByLogin :one
SELECT * FROM role_grants WHERE lower(login) = lower(@login::text);

-- name: Grants :many
SELECT * FROM role_grants ORDER BY lower(login);

-- name: PutGrant :exec
INSERT INTO role_grants (login, role, granted_by) VALUES ($1, $2, $3)
ON CONFLICT (login) DO UPDATE SET role = EXCLUDED.role, granted_by = EXCLUDED.granted_by;

-- name: DeleteGrant :exec
DELETE FROM role_grants WHERE lower(login) = lower(@login::text);
