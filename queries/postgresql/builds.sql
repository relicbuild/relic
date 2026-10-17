-- name: InsertBuild :exec
INSERT INTO builds (id, project, name, number, started, started_ms, info, created_by)
VALUES (@id, @project, @name, @number, @started, @started_ms, @info, @created_by);

-- name: InsertBuildArtifacts :exec
INSERT INTO build_artifacts (build_id, module_id, name, type, path, sha1, sha256, md5)
SELECT @build_id, unnest(@module_ids::text[]), unnest(@names::text[]), unnest(@types::text[]), unnest(@paths::text[]),
       unnest(@sha1s::text[]), unnest(@sha256s::text[]), unnest(@md5s::text[]);

-- name: InsertBuildDependencies :exec
INSERT INTO build_dependencies (build_id, module_id, dep_id, type, scopes, sha1, sha256, md5)
SELECT @build_id, unnest(@module_ids::text[]), unnest(@dep_ids::text[]), unnest(@types::text[]), unnest(@scopes::text[]),
       unnest(@sha1s::text[]), unnest(@sha256s::text[]), unnest(@md5s::text[]);

-- name: FindBuildNames :many
SELECT DISTINCT name FROM builds WHERE project = @project AND name = ANY(@candidates::text[]);

-- name: ListBuildNames :many
SELECT name, max(started)::timestamptz AS last_started FROM builds
WHERE project = @project GROUP BY name ORDER BY name;

-- name: ListBuildRuns :many
SELECT id, number, started FROM builds
WHERE project = @project AND name = @name ORDER BY started DESC;

-- name: GetBuildRun :one
SELECT * FROM builds
WHERE project = @project AND name = @name AND number = @number
ORDER BY started DESC LIMIT 1;

-- name: ListBuildArtifacts :many
SELECT * FROM build_artifacts WHERE build_id = @build_id;

-- name: ListBuildDependencies :many
SELECT * FROM build_dependencies WHERE build_id = @build_id;

-- name: DeleteBuilds :exec
DELETE FROM builds WHERE id = ANY(@ids::uuid[]);

-- name: InsertBuildPromotion :exec
INSERT INTO build_promotions (id, build_id, status, comment, repository, user_name, created)
VALUES (@id, @build_id, @status, @comment, @repository, @user_name, @created);

-- name: ListBuildPromotions :many
SELECT * FROM build_promotions WHERE build_id = @build_id ORDER BY created;

-- name: FindItemsBySHA1 :many
SELECT sqlc.embed(i),
       EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND p.key = 'build.name' AND p.value = @build_name::text) AS has_build_name,
       EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND p.key = 'build.number' AND p.value = @build_number::text) AS has_build_number
FROM items i
WHERE i.type = 'file' AND i.sha1 = ANY(@sha1s::text[])
  AND (@source_repo::text = '' OR i.repo_key = @source_repo::text)
ORDER BY i.repo_key, i.path, i.name;

-- name: BuildsProducing :many
SELECT DISTINCT b.project, b.name, b.number, b.started, a.module_id
FROM build_artifacts a JOIN builds b ON b.id = a.build_id
WHERE a.sha1 = @sha1
ORDER BY b.started DESC
LIMIT 100;

-- name: BuildsUsing :many
SELECT DISTINCT b.project, b.name, b.number, b.started, d.module_id, d.scopes
FROM build_dependencies d JOIN builds b ON b.id = d.build_id
WHERE d.sha1 = @sha1
ORDER BY b.started DESC
LIMIT 100;
