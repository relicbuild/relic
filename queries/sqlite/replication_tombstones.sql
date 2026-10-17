-- name: PutTombstone :exec
INSERT INTO replication_tombstones (repo_key, path, deleted) VALUES (@repo_key, @path, @deleted)
ON CONFLICT (repo_key, path) DO UPDATE SET deleted = excluded.deleted;

-- name: DeleteTombstone :exec
DELETE FROM replication_tombstones WHERE repo_key = @repo_key AND path = @path;

-- name: ListTombstones :many
SELECT * FROM replication_tombstones WHERE repo_key = @repo_key ORDER BY path;

-- name: PurgeTombstones :execrows
DELETE FROM replication_tombstones WHERE deleted < @before;
