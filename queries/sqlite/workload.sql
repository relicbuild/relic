-- name: UpsertWorkloadIdentity :exec
INSERT INTO workload_identities (name, config, created, updated) VALUES (@name, CAST(@config AS TEXT), @now, @now)
ON CONFLICT (name) DO UPDATE SET config = excluded.config, updated = excluded.updated;

-- name: GetWorkloadIdentity :one
SELECT * FROM workload_identities WHERE name = @name;

-- name: ListWorkloadIdentities :many
SELECT * FROM workload_identities ORDER BY name;

-- name: DeleteWorkloadIdentity :execrows
DELETE FROM workload_identities WHERE name = @name;
