-- name: InsertAuditEvent :exec
INSERT INTO audit_events (id, time, actor, action, target, outcome, client, detail)
VALUES (@id, @time, @actor, @action, @target, @outcome, @client, @detail);

-- name: ListAuditEvents :many
-- Newest first.
SELECT * FROM audit_events
WHERE (@actor::text = '' OR actor = @actor)
  AND (@action::text = '' OR strpos(lower(action), lower(@action)) > 0)
  AND (@target::text = '' OR strpos(lower(target), lower(@target)) > 0)
  AND (@outcome::text = '' OR outcome = @outcome)
  AND time >= @since AND time < @until
ORDER BY id DESC
LIMIT @max_rows OFFSET @skip;

-- name: CountAuditEvents :one
SELECT count(*) FROM audit_events
WHERE (@actor::text = '' OR actor = @actor)
  AND (@action::text = '' OR strpos(lower(action), lower(@action)) > 0)
  AND (@target::text = '' OR strpos(lower(target), lower(@target)) > 0)
  AND (@outcome::text = '' OR outcome = @outcome)
  AND time >= @since AND time < @until;

-- name: DeleteAuditEventsBefore :execrows
DELETE FROM audit_events WHERE time < @cutoff;
