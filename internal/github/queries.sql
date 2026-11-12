-- name: GetApp :one
SELECT * FROM github_app LIMIT 1;

-- name: AppExists :one
SELECT EXISTS (SELECT 1 FROM github_app);

-- name: InsertApp :exec
INSERT INTO github_app (id, app_id, slug, org_login, client_id, client_secret_id, private_key_id, webhook_secret_id, html_url)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: UpsertInstallation :exec
INSERT INTO installations (installation_id, account_login) VALUES ($1, $2)
ON CONFLICT (installation_id) DO UPDATE SET account_login = EXCLUDED.account_login;

-- name: SetInstallationSuspended :exec
UPDATE installations SET suspended_at = $2 WHERE installation_id = $1;

-- name: Installations :many
SELECT * FROM installations ORDER BY account_login;

-- name: InsertDelivery :one
INSERT INTO webhook_deliveries (delivery_id, event, action, received_via)
VALUES ($1, $2, $3, $4)
ON CONFLICT (delivery_id) DO NOTHING
RETURNING delivery_id;

-- name: FinishDelivery :exec
UPDATE webhook_deliveries SET handled_at = now(), error = $2 WHERE delivery_id = $1;

-- name: DeleteDelivery :exec
DELETE FROM webhook_deliveries WHERE delivery_id = $1;

-- name: KnownDeliveries :many
SELECT delivery_id FROM webhook_deliveries WHERE delivery_id = ANY(@ids::text[]);

-- name: RecentDeliveries :many
SELECT * FROM webhook_deliveries ORDER BY received_at DESC LIMIT $1;

-- name: LastDelivery :one
SELECT * FROM webhook_deliveries ORDER BY received_at DESC LIMIT 1;

-- name: RequestRedelivery :exec
INSERT INTO webhook_redeliveries (delivery_id) VALUES ($1) ON CONFLICT (delivery_id) DO UPDATE SET requested_at = now();

-- name: IsRedelivery :one
SELECT EXISTS (SELECT 1 FROM webhook_redeliveries WHERE delivery_id = $1);

-- name: WebhookSecretID :one
SELECT webhook_secret_id FROM github_app LIMIT 1;

-- name: HasActiveRound :one
SELECT EXISTS (
    SELECT 1 FROM rounds r JOIN reviews v ON v.id = r.review_id
    WHERE v.repository_id = $1 AND v.pr_number = $2 AND r.head_sha = $3
      AND r.status IN ('queued', 'running', 'publishing', 'done'));
