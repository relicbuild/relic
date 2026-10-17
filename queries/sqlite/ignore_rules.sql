-- name: InsertIgnoreRule :exec
INSERT INTO scan_ignore_rules (id, doc, expires_at, created, created_by) VALUES (@id, CAST(@doc AS TEXT), @expires_at, @now, @username);

-- name: GetIgnoreRule :one
SELECT * FROM scan_ignore_rules WHERE id = @id;

-- name: ListIgnoreRules :many
SELECT * FROM scan_ignore_rules ORDER BY created DESC;

-- name: ListActiveIgnoreRules :many
SELECT * FROM scan_ignore_rules WHERE expires_at IS NULL OR expires_at > @now ORDER BY created;

-- name: DeleteIgnoreRule :execrows
DELETE FROM scan_ignore_rules WHERE id = @id;
