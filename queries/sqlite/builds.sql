-- name: InsertBuild :exec
INSERT INTO builds (id, project, name, number, started, started_ms, info, created_by)
VALUES (@id, @project, @name, @number, @started, @started_ms, CAST(@info AS TEXT), @created_by);

-- name: InsertBuildArtifacts :exec
-- The columns come as parallel JSON arrays, zipped by index.
WITH given_module_ids AS MATERIALIZED (SELECT key, value FROM json_each(@module_ids)),
     given_names AS MATERIALIZED (SELECT key, value FROM json_each(@names)),
     given_types AS MATERIALIZED (SELECT key, value FROM json_each(@types)),
     given_paths AS MATERIALIZED (SELECT key, value FROM json_each(@paths)),
     given_sha1s AS MATERIALIZED (SELECT key, value FROM json_each(@sha1s)),
     given_sha256s AS MATERIALIZED (SELECT key, value FROM json_each(@sha256s)),
     given_md5s AS MATERIALIZED (SELECT key, value FROM json_each(@md5s))
INSERT INTO build_artifacts (build_id, module_id, name, type, path, sha1, sha256, md5)
SELECT @build_id, m.value, n.value, t.value, p.value, s1.value, s256.value, m5.value
FROM given_module_ids AS m
JOIN given_names AS n ON n.key = m.key
JOIN given_types AS t ON t.key = m.key
JOIN given_paths AS p ON p.key = m.key
JOIN given_sha1s AS s1 ON s1.key = m.key
JOIN given_sha256s AS s256 ON s256.key = m.key
JOIN given_md5s AS m5 ON m5.key = m.key
ORDER BY m.key;

-- name: InsertBuildDependencies :exec
-- The columns come as parallel JSON arrays, zipped by index.
WITH given_module_ids AS MATERIALIZED (SELECT key, value FROM json_each(@module_ids)),
     given_dep_ids AS MATERIALIZED (SELECT key, value FROM json_each(@dep_ids)),
     given_types AS MATERIALIZED (SELECT key, value FROM json_each(@types)),
     given_scopes AS MATERIALIZED (SELECT key, value FROM json_each(@scopes)),
     given_sha1s AS MATERIALIZED (SELECT key, value FROM json_each(@sha1s)),
     given_sha256s AS MATERIALIZED (SELECT key, value FROM json_each(@sha256s)),
     given_md5s AS MATERIALIZED (SELECT key, value FROM json_each(@md5s))
INSERT INTO build_dependencies (build_id, module_id, dep_id, type, scopes, sha1, sha256, md5)
SELECT @build_id, m.value, d.value, t.value, sc.value, s1.value, s256.value, m5.value
FROM given_module_ids AS m
JOIN given_dep_ids AS d ON d.key = m.key
JOIN given_types AS t ON t.key = m.key
JOIN given_scopes AS sc ON sc.key = m.key
JOIN given_sha1s AS s1 ON s1.key = m.key
JOIN given_sha256s AS s256 ON s256.key = m.key
JOIN given_md5s AS m5 ON m5.key = m.key
ORDER BY m.key;

-- name: FindBuildNames :many
SELECT DISTINCT name FROM builds
WHERE project = @project AND name IN (SELECT value FROM json_each(@candidates));

-- name: ListBuildNames :many
SELECT name, max(started) AS last_started FROM builds
WHERE project = @project GROUP BY name ORDER BY name;

-- name: ListBuildRuns :many
SELECT id, number, started FROM builds
WHERE project = @project AND name = @name ORDER BY started DESC;

-- name: GetBuildRun :one
SELECT * FROM builds
WHERE project = @project AND name = @name AND number = @number
ORDER BY started DESC, id DESC LIMIT 1;

-- name: ListBuildRunIDsStartedAt :many
SELECT id FROM builds
WHERE project = @project AND name = @name AND number = @number AND started_ms = @started_ms;

-- name: MoveBuildPromotions :exec
UPDATE build_promotions SET build_id = @to_build_id
WHERE build_id IN (SELECT value FROM json_each(@from_build_ids));

-- name: ListBuildArtifacts :many
SELECT * FROM build_artifacts WHERE build_id = @build_id;

-- name: ListBuildDependencies :many
SELECT * FROM build_dependencies WHERE build_id = @build_id;

-- name: DeleteBuilds :exec
DELETE FROM builds WHERE id IN (SELECT value FROM json_each(@ids));

-- name: InsertBuildPromotion :exec
INSERT INTO build_promotions (id, build_id, status, comment, repository, user_name, created)
VALUES (@id, @build_id, @status, @comment, @repository, @user_name, @created);

-- name: ListBuildPromotions :many
SELECT * FROM build_promotions WHERE build_id = @build_id ORDER BY created;

-- name: FindItemsBySHA1 :many
SELECT i.*,
       EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND p.key = 'build.name' AND p.value = @build_name) AS has_build_name,
       EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND p.key = 'build.number' AND p.value = @build_number) AS has_build_number,
       EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = i.id
               AND ((p.key = 'build.name' AND p.value <> @build_name)
                 OR (p.key = 'build.number' AND p.value <> @build_number))) AS has_other_build
FROM items i
WHERE i.type = 'file' AND i.sha1 IN (SELECT value FROM json_each(@sha1s))
  AND (@source_repo = '' OR i.repo_key = @source_repo)
  AND NOT EXISTS (SELECT 1 FROM repositories r WHERE r.key = i.repo_key AND r.format IN (SELECT value FROM json_each(@excluded_formats)))
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
