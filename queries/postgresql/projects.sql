-- name: UpsertProject :exec
INSERT INTO projects (key, config, created, updated) VALUES (@key, @config, @now, @now)
ON CONFLICT (key) DO UPDATE SET config = EXCLUDED.config, updated = EXCLUDED.updated;

-- name: GetProject :one
SELECT * FROM projects WHERE key = @key;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY key;

-- name: DeleteProject :execrows
DELETE FROM projects WHERE key = @key;

-- name: ListRepositoryProjects :many
SELECT key, project FROM repositories WHERE project <> '' ORDER BY key;

-- name: ProjectUsedBytes :one
SELECT coalesce(sum(i.size), 0)::bigint AS bytes
FROM items i JOIN repositories r ON r.key = i.repo_key
WHERE r.project = @project AND i.type = 'file';
