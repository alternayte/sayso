-- name: UpsertRepository :one
INSERT INTO repositories (id, github_id, installation_id, full_name, private)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (github_id) DO UPDATE
SET installation_id = EXCLUDED.installation_id, full_name = EXCLUDED.full_name,
    private = EXCLUDED.private, removed_at = NULL
RETURNING *;

-- name: RemoveRepository :exec
UPDATE repositories SET removed_at = now() WHERE github_id = $1 AND removed_at IS NULL;

-- name: RemoveInstallationRepositories :exec
UPDATE repositories SET removed_at = now() WHERE installation_id = $1 AND removed_at IS NULL;

-- name: SetDefaultBranch :exec
UPDATE repositories SET default_branch = $2 WHERE github_id = $1 AND default_branch <> $2;

-- name: ByGitHubID :one
SELECT * FROM repositories WHERE github_id = $1 AND removed_at IS NULL;

-- name: ByFullName :one
SELECT * FROM repositories WHERE lower(full_name) = lower(@full_name) AND removed_at IS NULL;

-- name: List :many
SELECT * FROM repositories WHERE removed_at IS NULL ORDER BY lower(full_name);

-- name: Active :many
SELECT * FROM repositories WHERE removed_at IS NULL AND mode <> 'off' ORDER BY lower(full_name);

-- name: SetMode :one
UPDATE repositories
SET mode = @mode, enabled_at = CASE WHEN @mode <> 'off' AND enabled_at IS NULL THEN now() ELSE enabled_at END
WHERE id = @id
RETURNING *;
