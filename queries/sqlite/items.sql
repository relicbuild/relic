-- name: GetItem :one
SELECT * FROM items WHERE repo_key = @repo_key AND path = @path AND name = @name;

-- name: GetItemForUpdate :one
SELECT * FROM items WHERE repo_key = @repo_key AND path = @path AND name = @name;

-- name: InsertFile :one
INSERT INTO items (id, repo_key, path, name, type, size, sha1, sha256, md5,
                   created, created_by, modified, modified_by, updated)
VALUES (@id, @repo_key, @path, @name, 'file', @size, @sha1, @sha256, @md5,
        @now, @username, @now, @username, @now)
ON CONFLICT (repo_key, path, name) DO NOTHING
RETURNING id;

-- name: OverwriteFile :exec
UPDATE items
SET size = @size, sha1 = @sha1, sha256 = @sha256, md5 = @md5,
    modified = @now, modified_by = @username, updated = @now
WHERE id = @id;

-- name: EnsureFolders :many
INSERT INTO items (id, repo_key, path, name, type, created, created_by, modified, modified_by, updated)
SELECT w.value, @repo_key, p.value, n.value, 'folder', @now, @username, @now, @username, @now
FROM json_each(@ids) AS w
JOIN json_each(@paths) AS p ON p.key = w.key
JOIN json_each(@names) AS n ON n.key = w.key
WHERE true
ON CONFLICT (repo_key, path, name) DO NOTHING;
SELECT i.path, i.name FROM items i
JOIN json_each(@paths) AS p ON i.path = p.value
JOIN json_each(@names) AS n ON n.key = p.key AND i.name = n.value
WHERE i.repo_key = @repo_key AND i.type = 'file';

-- name: ListChildren :many
SELECT * FROM items WHERE repo_key = @repo_key AND path = @path ORDER BY name;

-- name: ListFilesUnder :many
-- prefix is the LIKE-escaped folder path + '/%'.
SELECT * FROM items
WHERE repo_key = @repo_key AND type = 'file'
  AND (path = @folder OR path LIKE @prefix ESCAPE '\' OR @folder = '.')
ORDER BY path, name;

-- name: DeleteSubtree :many
DELETE FROM items
WHERE repo_key = @repo_key
  AND ((path = @path AND name = @name) OR path = @full_path OR path LIKE @prefix ESCAPE '\')
RETURNING sha256;

-- name: DeleteRepositoryItems :many
DELETE FROM items WHERE repo_key = @repo_key RETURNING sha256;

-- name: GetItemProperties :many
SELECT key, value FROM item_properties WHERE item_id = @item_id ORDER BY key, value;

-- name: DeleteItemProperties :exec
DELETE FROM item_properties WHERE item_id = @item_id;

-- name: DeleteItemPropertyKeys :exec
DELETE FROM item_properties WHERE item_id = @item_id AND key IN (SELECT value FROM json_each(@keys));

-- name: AddItemProperties :exec
-- The keys and values come as parallel JSON arrays. WHERE true lets SQLite
-- parse the ON CONFLICT clause after a join.
INSERT INTO item_properties (item_id, key, value)
SELECT @item_id, k.value, v.value
FROM json_each(@keys) AS k
JOIN json_each(@vals) AS v ON v.key = k.key
WHERE true
ON CONFLICT DO NOTHING;

-- name: ListSubtree :many
SELECT * FROM items
WHERE repo_key = @repo_key
  AND ((path = @path AND name = @name) OR path = @full_path OR path LIKE @prefix ESCAPE '\')
ORDER BY path, name;

-- name: ListRepositoryItems :many
SELECT * FROM items WHERE repo_key = @repo_key ORDER BY path, name;

-- name: FindFilesBySha256 :many
-- Files in a repository with the given content, below prefix (LIKE-escaped
-- folder path + '/%', or '%' for the whole repository).
SELECT * FROM items
WHERE repo_key = @repo_key AND sha256 = @sha256 AND type = 'file' AND path LIKE @prefix ESCAPE '\'
ORDER BY path, name
LIMIT 50;

-- name: FindFilesNamed :many
-- Files with one of the given names below prefix (as for FindFilesBySha256).
SELECT * FROM items
WHERE repo_key = @repo_key AND type = 'file' AND name IN (SELECT value FROM json_each(@names))
  AND path LIKE @prefix ESCAPE '\'
ORDER BY path, name;

-- name: FindFilesByProperty :many
SELECT i.* FROM items i
WHERE i.repo_key = @repo_key AND i.type = 'file' AND EXISTS (
    SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND p.key = @key AND lower(p.value) = lower(@value)
)
ORDER BY i.path, i.name;

-- name: FindFilesByContent :many
SELECT i.* FROM items i JOIN blobs b ON b.sha256 = i.sha256
WHERE i.type = 'file'
  AND ((@sha256 <> '' AND i.sha256 = @sha256) OR (@sha1 <> '' AND b.sha1 = @sha1))
LIMIT 100;

-- name: SearchItemsByName :many
-- Items of one repository whose name contains a term (case-insensitive
-- beyond ASCII, as ILIKE is; the caller escapes LIKE wildcards). ESCAPE '\'
-- is PostgreSQL's default, which SQLite lacks.
SELECT * FROM items
WHERE repo_key = @repo_key AND unicode_lower(name) LIKE unicode_lower(@pattern) ESCAPE '\' AND name <> '.'
ORDER BY path, name
LIMIT @max_rows;

-- name: ListItemsWithProperty :many
SELECT i.*, p.value AS property_value FROM items i
JOIN item_properties p ON p.item_id = i.id
WHERE i.repo_key = @repo_key AND p.key = @key
ORDER BY p.value DESC, i.path, i.name;

-- name: ListStaleFilesInFolders :many
-- Files modified before @before in folders named @folder, in repositories
-- of the given formats.
SELECT f.repo_key, f.path, f.name
FROM items d
JOIN repositories r ON r.key = d.repo_key AND r.format IN (SELECT value FROM json_each(CAST(@formats AS TEXT)))
JOIN items f ON f.repo_key = d.repo_key
    AND f.path = CASE WHEN d.path = '.' THEN d.name ELSE d.path || '/' || d.name END
WHERE d.type = 'folder' AND d.name = @folder AND f.type = 'file' AND f.modified < @before
ORDER BY f.repo_key, f.path, f.name
LIMIT @max;
