-- name: UpsertPromotionGate :exec
INSERT INTO promotion_gates (name, environment, condition, description, created, updated)
VALUES (@name, @environment, @condition, @description, @now, @now)
ON CONFLICT (name) DO UPDATE SET environment = excluded.environment, condition = excluded.condition,
    description = excluded.description, updated = excluded.updated;

-- name: GetPromotionGate :one
SELECT * FROM promotion_gates WHERE name = @name;

-- name: ListPromotionGates :many
SELECT * FROM promotion_gates ORDER BY name;

-- name: DeletePromotionGate :execrows
DELETE FROM promotion_gates WHERE name = @name;
