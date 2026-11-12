-- name: GetApp :one
SELECT * FROM github_app LIMIT 1;

-- name: AppExists :one
SELECT EXISTS (SELECT 1 FROM github_app);

-- name: InsertApp :exec
INSERT INTO github_app (id, app_id, slug, org_login, client_id, client_secret_id, private_key_id, webhook_secret_id, html_url)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);
