-- name: GetItem :one
SELECT * FROM items WHERE repo_key = @repo_key AND path = @path AND name = @name;

-- name: ListItemsAt :many
SELECT * FROM items
WHERE repo_key = @repo_key
  AND (path, name) IN (SELECT unnest(@paths::text[]), unnest(@names::text[]));

-- name: GetItemForUpdate :one
SELECT * FROM items WHERE repo_key = @repo_key AND path = @path AND name = @name FOR UPDATE;

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

-- name: SetItemCreated :exec
UPDATE items SET created = @created, created_by = @created_by WHERE id = @id;

-- name: EnsureFolders :many
WITH wanted AS (
    SELECT unnest(@ids::uuid[]) AS id, unnest(@paths::text[]) AS path, unnest(@names::text[]) AS name
), inserted AS (
    INSERT INTO items (id, repo_key, path, name, type, created, created_by, modified, modified_by, updated)
    SELECT w.id, @repo_key, w.path, w.name, 'folder', @now, @username, @now, @username, @now
    FROM wanted w
    ON CONFLICT (repo_key, path, name) DO NOTHING
)
SELECT i.path, i.name FROM items i JOIN wanted w ON i.path = w.path AND i.name = w.name
WHERE i.repo_key = @repo_key AND i.type = 'file';

-- name: ListChildren :many
SELECT * FROM items WHERE repo_key = @repo_key AND path = @path ORDER BY name;

-- name: ListFilesUnder :many
-- prefix is the LIKE-escaped folder path + '/%'.
SELECT * FROM items
WHERE repo_key = @repo_key AND type = 'file'
  AND (path = @folder OR path LIKE @prefix::text ESCAPE '\' OR @folder::text = '.')
ORDER BY path, name;

-- name: DeleteSubtree :many
DELETE FROM items
WHERE repo_key = @repo_key
  AND ((path = @path AND name = @name) OR path = @full_path OR path LIKE @prefix::text ESCAPE '\')
RETURNING sha256;

-- name: DeleteEmptyFolder :execrows
DELETE FROM items
WHERE items.repo_key = @repo_key AND items.path = @path AND items.name = @name AND items.type = 'folder'
  AND NOT EXISTS (SELECT 1 FROM items children WHERE children.repo_key = @repo_key AND children.path = @full_path);

-- name: DeleteRepositoryItems :many
DELETE FROM items WHERE repo_key = @repo_key RETURNING sha256;

-- name: GetItemProperties :many
SELECT key, value FROM item_properties WHERE item_id = @item_id ORDER BY key, value;

-- name: DeleteItemProperties :exec
DELETE FROM item_properties WHERE item_id = @item_id;

-- name: DeleteItemPropertyKeys :exec
DELETE FROM item_properties WHERE item_id = @item_id AND key = ANY(@keys::text[]);

-- name: AddItemProperties :exec
INSERT INTO item_properties (item_id, key, value)
SELECT @item_id, unnest(@keys::text[]), unnest(@vals::text[])
ON CONFLICT DO NOTHING;

-- name: ListSubtree :many
SELECT * FROM items
WHERE repo_key = @repo_key
  AND ((path = @path AND name = @name) OR path = @full_path OR path LIKE @prefix::text ESCAPE '\')
ORDER BY path, name;

-- name: ListRepositoryItems :many
SELECT * FROM items WHERE repo_key = @repo_key ORDER BY path, name;

-- name: ListSubtreePage :many
-- ListSubtree's items after (@after_path, @after_name), at most @max.
SELECT * FROM items
WHERE repo_key = @repo_key
  AND ((path = @path AND name = @name) OR path = @full_path OR path LIKE @prefix::text ESCAPE '\')
  AND (path, name) > (@after_path::text, @after_name::text)
ORDER BY path, name
LIMIT @max;

-- name: ListRepositoryItemsPage :many
-- ListRepositoryItems' items after (@after_path, @after_name), at most @max.
SELECT * FROM items
WHERE repo_key = @repo_key AND (path, name) > (@after_path::text, @after_name::text)
ORDER BY path, name
LIMIT @max;

-- name: FindFilesBySha256 :many
-- Files in a repository with the given content, below prefix (LIKE-escaped
-- folder path + '/%', or '%' for the whole repository).
SELECT * FROM items
WHERE repo_key = @repo_key AND sha256 = @sha256 AND type = 'file' AND path LIKE @prefix::text ESCAPE '\'
ORDER BY path, name
LIMIT 50;

-- name: FindFilesNamed :many
-- Files with one of the given names below prefix (as for FindFilesBySha256).
SELECT * FROM items
WHERE repo_key = @repo_key AND type = 'file' AND name = ANY(@names::text[]) AND path LIKE @prefix::text ESCAPE '\'
ORDER BY path, name;

-- name: FindFilesByProperty :many
SELECT i.* FROM items i
WHERE i.repo_key = @repo_key AND i.type = 'file' AND EXISTS (
    SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND p.key = @key AND lower(p.value) = lower(@value::text)
)
ORDER BY i.path, i.name;

-- name: FindFilesByContent :many
SELECT i.* FROM items i JOIN blobs b ON b.sha256 = i.sha256
WHERE i.type = 'file'
  AND ((@sha256::text <> '' AND i.sha256 = @sha256::text) OR (@sha1::text <> '' AND b.sha1 = @sha1::text))
LIMIT 100;

-- name: SearchItemsByName :many
-- Items of one repository whose name contains a term (case-insensitive;
-- the caller escapes LIKE wildcards).
SELECT * FROM items
WHERE repo_key = @repo_key AND name ILIKE @pattern AND name <> '.'
ORDER BY path, name
LIMIT @max_rows;

-- name: ListItemsWithProperty :many
SELECT sqlc.embed(i), p.value AS property_value FROM items i
JOIN item_properties p ON p.item_id = i.id
WHERE i.repo_key = @repo_key AND p.key = @key
ORDER BY p.value DESC, i.path, i.name;

-- name: ListStaleFilesInFolders :many
-- Files modified before @before in folders named @folder, in repositories
-- of the given formats.
SELECT f.repo_key, f.path, f.name
FROM items d
JOIN repositories r ON r.key = d.repo_key AND r.format = ANY(@formats::text[])
JOIN items f ON f.repo_key = d.repo_key
    AND f.path = CASE WHEN d.path = '.' THEN d.name ELSE d.path || '/' || d.name END
WHERE d.type = 'folder' AND d.name = @folder AND f.type = 'file' AND f.modified < @before::timestamptz
ORDER BY f.repo_key, f.path, f.name
LIMIT @max;
