-- name: PutHold :exec
INSERT INTO item_holds (repo_key, path, reason, created_by, created)
VALUES (@repo_key, @path, @reason, @created_by, @created)
ON CONFLICT (repo_key, path) DO UPDATE SET reason = excluded.reason;

-- name: DeleteHold :execrows
DELETE FROM item_holds WHERE repo_key = @repo_key AND path = @path;

-- name: ListHolds :many
SELECT * FROM item_holds ORDER BY repo_key, path;

-- name: HoldsTouching :many
-- Holds on the path, on a folder above it (an empty path is the repository's
-- root), or on anything under it; compared as strings, not LIKE patterns.
SELECT * FROM item_holds
WHERE repo_key = @repo_key AND (@path = '' OR path = '' OR path = @path
  OR substr(@path, 1, length(path) + 1) = path || '/'
  OR substr(path, 1, length(@path) + 1) = @path || '/')
ORDER BY path;
