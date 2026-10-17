-- name: UpsertWebhook :exec
INSERT INTO webhooks (name, config, created, updated) VALUES (@name, @config, @now, @now)
ON CONFLICT (name) DO UPDATE SET config = EXCLUDED.config, updated = EXCLUDED.updated;

-- name: GetWebhook :one
SELECT * FROM webhooks WHERE name = @name;

-- name: ListWebhooks :many
SELECT * FROM webhooks ORDER BY name;

-- name: DeleteWebhook :execrows
DELETE FROM webhooks WHERE name = @name;

-- name: InsertWebhookDelivery :exec
INSERT INTO webhook_deliveries (id, webhook, event, payload, status, next_attempt, created)
VALUES (@id, @webhook, @event, @payload, 'pending', @now, @now);

-- name: ClaimWebhookDeliveries :many
-- Leases due deliveries until lease_until, so no other worker sends them.
UPDATE webhook_deliveries SET next_attempt = @lease_until
WHERE id IN (SELECT d.id FROM webhook_deliveries d
             WHERE d.status = 'pending' AND d.next_attempt <= @now
             ORDER BY d.next_attempt LIMIT @max_rows FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: FinishWebhookDelivery :exec
UPDATE webhook_deliveries
SET status = @status, attempts = @attempts, next_attempt = @next_attempt, response_status = @response_status,
    error = @error, delivered = @delivered
WHERE id = @id;

-- name: ListWebhookDeliveries :many
SELECT * FROM webhook_deliveries
WHERE (@webhook::text = '' OR webhook = @webhook)
ORDER BY created DESC
LIMIT @max_rows;

-- name: PurgeWebhookDeliveries :execrows
DELETE FROM webhook_deliveries WHERE status <> 'pending' AND created < @before;
