-- name: ListTrashEntries :many
SELECT i.*,
    t.value AS deleted,
    coalesce((SELECT min(p.value) FROM item_properties p WHERE p.item_id = i.id AND p.key = @deleted_by_key), '') AS deleted_by,
    coalesce((SELECT min(p.value) FROM item_properties p WHERE p.item_id = i.id AND p.key = @original_repository_key), '') AS original_repository,
    coalesce((SELECT min(p.value) FROM item_properties p WHERE p.item_id = i.id AND p.key = @original_path_key), '') AS original_path
FROM items i
JOIN item_properties t ON t.item_id = i.id AND t.key = @time_key
WHERE i.repo_key = @repo_key
  AND (@kind = '' OR i.type = @kind)
  AND (@deleted_by = '' OR EXISTS (
    SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND p.key = @deleted_by_key AND p.value = @deleted_by))
  AND (@original_repository = '' OR EXISTS (
    SELECT 1 FROM item_properties p
    WHERE p.item_id = i.id AND p.key = @original_repository_key AND p.value = @original_repository))
  AND (@query = '' OR instr(lower(i.path || '/' || i.name), lower(@query)) > 0 OR EXISTS (
    SELECT 1 FROM item_properties p
    WHERE p.item_id = i.id AND p.key = @deleted_by_key AND instr(lower(p.value), lower(@query)) > 0))
ORDER BY t.value DESC, i.path, i.name
LIMIT @max_rows OFFSET @skip;

-- name: CountTrashEntries :one
SELECT count(*) FROM items i
JOIN item_properties t ON t.item_id = i.id AND t.key = @time_key
WHERE i.repo_key = @repo_key
  AND (@kind = '' OR i.type = @kind)
  AND (@deleted_by = '' OR EXISTS (
    SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND p.key = @deleted_by_key AND p.value = @deleted_by))
  AND (@original_repository = '' OR EXISTS (
    SELECT 1 FROM item_properties p
    WHERE p.item_id = i.id AND p.key = @original_repository_key AND p.value = @original_repository))
  AND (@query = '' OR instr(lower(i.path || '/' || i.name), lower(@query)) > 0 OR EXISTS (
    SELECT 1 FROM item_properties p
    WHERE p.item_id = i.id AND p.key = @deleted_by_key AND instr(lower(p.value), lower(@query)) > 0));

-- name: ListTrashPropertyValues :many
SELECT DISTINCT p.value FROM items i
JOIN item_properties t ON t.item_id = i.id AND t.key = @time_key
JOIN item_properties p ON p.item_id = i.id AND p.key = @key
WHERE i.repo_key = @repo_key
ORDER BY p.value;
