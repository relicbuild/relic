-- name: UpsertWorkloadIdentity :exec
INSERT INTO workload_identities (name, config, created, updated) VALUES (@name, @config, @now, @now)
ON CONFLICT (name) DO UPDATE SET config = EXCLUDED.config, updated = EXCLUDED.updated;

-- name: GetWorkloadIdentity :one
SELECT * FROM workload_identities WHERE name = @name;

-- name: ListWorkloadIdentities :many
SELECT * FROM workload_identities ORDER BY name;

-- name: DeleteWorkloadIdentity :execrows
DELETE FROM workload_identities WHERE name = @name;
