-- name: CreateRelease :exec
INSERT INTO releases (id, project, repository, name, version, status, draft, artifacts, created_by, created)
VALUES (@id, @project, @repository, @name, @version, @status, @draft, CAST(@artifacts AS TEXT), @created_by, @created);

-- name: GetRelease :one
SELECT * FROM releases WHERE repository = @repository AND name = @name AND version = @version;

-- name: UpdateReleaseContent :exec
UPDATE releases SET artifacts = CAST(@artifacts AS TEXT), draft = @draft, status = @status WHERE id = @id;

-- name: SetReleaseTag :exec
UPDATE releases SET tag = @tag WHERE id = @id;

-- name: DeleteRelease :exec
DELETE FROM releases WHERE id = @id;

-- name: ListReleaseVersions :many
SELECT * FROM releases WHERE repository = @repository AND name = @name ORDER BY created DESC;

-- name: ListReleaseGroups :many
SELECT id, project, repository, name, version, status, draft, tag, artifacts, created_by, created, versions_count
FROM (
    SELECT *,
           count(*) OVER (PARTITION BY repository, name) AS versions_count,
           row_number() OVER (PARTITION BY repository, name ORDER BY created DESC) AS rn
    FROM releases
)
WHERE rn = 1
ORDER BY repository, name;

-- name: InsertReleasePromotion :exec
INSERT INTO release_promotions (id, release_id, environment, status, included_repositories, excluded_repositories, messages, created_by, created, created_millis)
VALUES (@id, @release_id, @environment, @status, @included_repositories, @excluded_repositories, CAST(@messages AS TEXT), @created_by, @created, @created_millis);

-- name: ListReleasePromotions :many
SELECT * FROM release_promotions WHERE release_id = @release_id ORDER BY created DESC;

-- name: DeleteReleasePromotion :execrows
DELETE FROM release_promotions WHERE release_id = @release_id AND created_millis = @created_millis;
