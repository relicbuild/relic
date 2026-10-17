-- name: UpsertNpmVersion :exec
INSERT INTO npm_versions (item_id, repo_key, name, version, manifest, integrity)
VALUES (@item_id, @repo_key, @name, @version, @manifest, @integrity)
ON CONFLICT (item_id) DO UPDATE
SET name = EXCLUDED.name, version = EXCLUDED.version, manifest = EXCLUDED.manifest, integrity = EXCLUDED.integrity;

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
ON CONFLICT (repo_key, name, tag) DO UPDATE SET version = EXCLUDED.version;

-- name: DeleteNpmDistTag :execrows
DELETE FROM npm_dist_tags WHERE repo_key = @repo_key AND name = @name AND tag = @tag;

-- name: SearchNpmPackages :many
SELECT DISTINCT ON (v.name) v.name, v.version, v.manifest, v.created, i.created_by AS publisher
FROM npm_versions v JOIN items i ON i.id = v.item_id
WHERE v.repo_key = ANY(@repo_keys::text[]) AND v.name ILIKE ALL(@patterns::text[])
ORDER BY v.name, v.created DESC
LIMIT @max;

-- name: SearchNpmVersions :many
SELECT v.name, v.version, v.manifest, v.created, i.created_by AS publisher, v.repo_key,
       i.path AS item_path, i.name AS item_name
FROM npm_versions v JOIN items i ON i.id = v.item_id
WHERE v.repo_key = ANY(@repo_keys::text[]) AND v.name ILIKE ALL(@patterns::text[])
ORDER BY v.name, v.created DESC
LIMIT @max;

-- name: SetNpmDeprecated :execrows
UPDATE npm_versions
SET manifest = CASE WHEN @message::text = '' THEN manifest - 'deprecated'
                    ELSE jsonb_set(manifest, '{deprecated}', to_jsonb(@message::text)) END
WHERE repo_key = @repo_key AND name = @name AND version = @version;
