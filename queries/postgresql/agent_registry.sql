-- name: UpsertRegistryEntry :exec
INSERT INTO registry_entries (repo_key, path, kind, name, version, description, sha256, doc, updated)
VALUES (@repo_key, @path, @kind, @name, @version, @description, @sha256, @doc, @updated)
ON CONFLICT (repo_key, path) DO UPDATE SET kind = EXCLUDED.kind, name = EXCLUDED.name, version = EXCLUDED.version,
    description = EXCLUDED.description, sha256 = EXCLUDED.sha256, doc = EXCLUDED.doc, updated = EXCLUDED.updated;

-- name: DeleteRegistryEntries :exec
DELETE FROM registry_entries
WHERE repo_key = @repo_key AND (@path::text = '' OR path = @path OR path LIKE @prefix::text ESCAPE '\');

-- name: DeleteRegistryApprovals :exec
DELETE FROM approvals
WHERE repo_key = @repo_key AND (@path::text = '' OR path = @path OR path LIKE @prefix::text ESCAPE '\');

-- name: ListRegistryEntries :many
SELECT e.repo_key, e.path, e.kind, e.name, e.version, e.description, e.sha256, e.doc, e.updated,
       coalesce(a.approved_by, '')::text AS approved_by, a.approved AS approved
FROM registry_entries e
LEFT JOIN approvals a ON a.repo_key = e.repo_key AND a.path = e.path AND a.sha256 = e.sha256
WHERE e.repo_key = @repo_key
ORDER BY e.name, e.updated DESC;

-- name: GetRegistryEntry :one
SELECT * FROM registry_entries WHERE repo_key = @repo_key AND path = @path;

-- name: PutApproval :exec
INSERT INTO approvals (repo_key, path, sha256, approved_by, approved)
VALUES (@repo_key, @path, @sha256, @approved_by, @approved)
ON CONFLICT (repo_key, path) DO UPDATE SET sha256 = EXCLUDED.sha256, approved_by = EXCLUDED.approved_by,
    approved = EXCLUDED.approved;

-- name: DeleteApproval :execrows
DELETE FROM approvals WHERE repo_key = @repo_key AND path = @path;

-- name: RegistryEntryApproved :one
-- Whether the registry entry at a path is indexed with the content sha256 and
-- has an approval of it; no row when the path is not an entry.
SELECT (a.sha256 IS NOT NULL AND e.sha256 = @sha256::text)::boolean AS approved FROM registry_entries e
LEFT JOIN approvals a ON a.repo_key = e.repo_key AND a.path = e.path AND a.sha256 = e.sha256
WHERE e.repo_key = @repo_key AND e.path = @path;
