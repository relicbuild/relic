-- name: UpsertReplication :exec
INSERT INTO replications (repository, config) VALUES (@repository, CAST(@config AS TEXT))
ON CONFLICT (repository) DO UPDATE SET config = excluded.config, updated = strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now');

-- name: UpdateReplication :execrows
UPDATE replications SET config = CAST(@config AS TEXT), updated = strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')
WHERE repository = @repository;

-- name: GetReplication :one
SELECT * FROM replications WHERE repository = @repository;

-- name: DeleteReplication :execrows
DELETE FROM replications WHERE repository = @repository;

-- name: ListReplications :many
SELECT * FROM replications ORDER BY repository;

-- name: RecordReplicationRun :exec
UPDATE replications SET last_run = @last_run, last_status = @last_status, last_error = @last_error
WHERE repository = @repository;
