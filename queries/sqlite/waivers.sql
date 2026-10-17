-- name: InsertWaiver :exec
INSERT INTO waivers (id, requested_by, reason, doc, status, created)
VALUES (@id, @requested_by, @reason, CAST(@doc AS TEXT), 'pending', @created);

-- name: GetWaiver :one
SELECT * FROM waivers WHERE id = @id;

-- name: ListWaivers :many
-- Newest first; requested_by '' lists everyone's.
SELECT * FROM waivers
WHERE (@requested_by = '' OR requested_by = @requested_by)
  AND (@status = '' OR status = @status)
ORDER BY created DESC, id DESC
LIMIT @max_rows OFFSET @skip;

-- name: CountWaivers :one
SELECT count(*) FROM waivers
WHERE (@requested_by = '' OR requested_by = @requested_by)
  AND (@status = '' OR status = @status);

-- name: DecideWaiver :execrows
UPDATE waivers
SET status = @status, decided_by = @decided_by, decision_note = @decision_note, decided = @decided,
    expires = @expires, ignore_rule_id = @ignore_rule_id
WHERE id = @id AND status = 'pending';

-- name: DeleteWaiver :execrows
DELETE FROM waivers WHERE id = @id AND requested_by = @requested_by AND status = 'pending';
