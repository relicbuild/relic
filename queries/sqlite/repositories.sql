-- name: CreateRepository :exec
INSERT INTO repositories (key, class, format, project, config)
VALUES (@key, @class, @format, @project, CAST(@config AS TEXT));

-- name: GetRepository :one
SELECT * FROM repositories WHERE key = @key;

-- name: ListRepositories :many
SELECT * FROM repositories
WHERE (@class IS NULL OR class = @class)
  AND (@format IS NULL OR format = @format)
ORDER BY key;

-- name: UpdateRepository :exec
UPDATE repositories
SET format = @format, project = @project, config = CAST(@config AS TEXT),
    updated = strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')
WHERE key = @key;

-- name: DeleteRepository :execrows
DELETE FROM repositories WHERE key = @key;

-- name: LockRepository :one
-- SQLite has one writer, so this only reads the key.
SELECT key FROM repositories WHERE key = @key;

-- name: RepositoryUsedBytes :one
SELECT CAST(coalesce(sum(size), 0) AS INTEGER) AS bytes FROM items WHERE repo_key = @repo_key AND type = 'file';
