-- name: UpsertCleanupPolicy :exec
INSERT INTO cleanup_policies (name, doc, created, updated) VALUES (@name, @doc, @now, @now)
ON CONFLICT (name) DO UPDATE SET doc = EXCLUDED.doc, updated = EXCLUDED.updated;

-- name: GetCleanupPolicy :one
SELECT * FROM cleanup_policies WHERE name = @name;

-- name: ListCleanupPolicies :many
SELECT * FROM cleanup_policies ORDER BY name;

-- name: DeleteCleanupPolicy :execrows
DELETE FROM cleanup_policies WHERE name = @name;

-- name: RecordCleanupRun :exec
UPDATE cleanup_policies SET last_run = @last_run, last_moved = @last_moved, last_error = @last_error
WHERE name = @name;

-- name: ListRepositoryDownloads :many
-- When each downloaded file of a repository was last downloaded.
SELECT i.path, i.name, d.last_downloaded
FROM items i
JOIN download_stats d ON d.item_id = i.id
WHERE i.repo_key = @repo_key;
