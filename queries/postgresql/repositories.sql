-- name: CreateRepository :exec
INSERT INTO repositories (key, class, format, project, config)
VALUES (@key, @class, @format, @project, @config);

-- name: GetRepository :one
SELECT * FROM repositories WHERE key = @key;

-- name: ListRepositories :many
SELECT * FROM repositories
WHERE (sqlc.narg(class)::text IS NULL OR class = sqlc.narg(class))
  AND (sqlc.narg(format)::text IS NULL OR format = sqlc.narg(format))
ORDER BY key;

-- name: UpdateRepository :exec
UPDATE repositories
SET format = @format, project = @project, config = @config, updated = now()
WHERE key = @key;

-- name: DeleteRepository :execrows
DELETE FROM repositories WHERE key = @key;

-- name: LockRepository :one
-- Serializes the deploys that check a repository's storage quota; NO KEY
-- UPDATE leaves the foreign key checks of item inserts unblocked.
SELECT key FROM repositories WHERE key = @key FOR NO KEY UPDATE;

-- name: RepositoryUsedBytes :one
SELECT coalesce(sum(size), 0)::bigint AS bytes FROM items WHERE repo_key = @repo_key AND type = 'file';
