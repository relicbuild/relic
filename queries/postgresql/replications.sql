-- name: UpsertReplication :exec
INSERT INTO replications (repository, config) VALUES (@repository, @config)
ON CONFLICT (repository) DO UPDATE SET config = EXCLUDED.config, updated = now();

-- name: UpdateReplication :execrows
UPDATE replications SET config = @config, updated = now() WHERE repository = @repository;

-- name: GetReplication :one
SELECT * FROM replications WHERE repository = @repository;

-- name: DeleteReplication :execrows
DELETE FROM replications WHERE repository = @repository;

-- name: ListReplications :many
SELECT * FROM replications ORDER BY repository;

-- name: RecordReplicationRun :exec
UPDATE replications SET last_run = @last_run, last_status = @last_status, last_error = @last_error
WHERE repository = @repository;
