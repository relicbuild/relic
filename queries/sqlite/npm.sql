-- name: UpsertNpmVersion :exec
INSERT INTO npm_versions (item_id, repo_key, name, version, manifest, integrity)
VALUES (@item_id, @repo_key, @name, @version, CAST(@manifest AS TEXT), @integrity)
ON CONFLICT (item_id) DO UPDATE
SET name = excluded.name, version = excluded.version, manifest = excluded.manifest, integrity = excluded.integrity;

-- name: NpmVersionExists :one
SELECT EXISTS (SELECT 1 FROM npm_versions WHERE repo_key = @repo_key AND name = @name AND version = @version);

-- name: ListNpmVersions :many
SELECT v.version, v.manifest, v.integrity, v.created, i.path AS item_path, i.name AS item_name, i.sha1, i.modified
FROM npm_versions v JOIN items i ON i.id = v.item_id
WHERE v.repo_key = @repo_key AND v.name = @name
ORDER BY v.created;

-- name: ListNpmDistTags :many
SELECT tag, version FROM npm_dist_tags WHERE repo_key = @repo_key AND name = @name ORDER BY tag;

-- name: SetNpmDistTag :exec
INSERT INTO npm_dist_tags (repo_key, name, tag, version) VALUES (@repo_key, @name, @tag, @version)
ON CONFLICT (repo_key, name, tag) DO UPDATE SET version = excluded.version;

-- name: DeleteNpmDistTag :execrows
DELETE FROM npm_dist_tags WHERE repo_key = @repo_key AND name = @name AND tag = @tag;

-- name: SearchNpmPackages :many
SELECT name, version, manifest FROM (
    SELECT v.name, v.version, v.manifest,
           row_number() OVER (PARTITION BY v.name ORDER BY v.created DESC) AS rn
    FROM npm_versions v
    WHERE v.repo_key IN (SELECT value FROM json_each(@repo_keys)) AND lower(v.name) LIKE lower(@pattern) ESCAPE '\'
)
WHERE rn = 1
ORDER BY name
LIMIT @max;

-- name: SearchNpmVersions :many
SELECT v.name, v.version, v.manifest, v.repo_key, i.path AS item_path, i.name AS item_name
FROM npm_versions v JOIN items i ON i.id = v.item_id
WHERE v.repo_key IN (SELECT value FROM json_each(@repo_keys)) AND lower(v.name) LIKE lower(@pattern) ESCAPE '\'
ORDER BY v.name, v.created DESC
LIMIT @max;

-- name: SetNpmDeprecated :execrows
UPDATE npm_versions
SET manifest = CASE WHEN @message = '' THEN json_remove(manifest, '$.deprecated')
                    ELSE json_set(manifest, '$.deprecated', @message) END
WHERE repo_key = @repo_key AND name = @name AND version = @version;
