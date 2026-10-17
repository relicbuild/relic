-- name: CreateRelease :exec
INSERT INTO releases (id, project, repository, name, version, status, draft, artifacts, created_by, created)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: GetRelease :one
SELECT * FROM releases WHERE repository = $1 AND name = $2 AND version = $3;

-- name: UpdateReleaseContent :exec
UPDATE releases SET artifacts = $2, draft = $3, status = $4 WHERE id = $1;

-- name: SetReleaseTag :exec
UPDATE releases SET tag = $2 WHERE id = $1;

-- name: DeleteRelease :exec
DELETE FROM releases WHERE id = $1;

-- name: ListReleaseVersions :many
SELECT * FROM releases WHERE repository = $1 AND name = $2 ORDER BY created DESC;

-- name: ListReleaseGroups :many
SELECT DISTINCT ON (repository, name) *, count(*) OVER (PARTITION BY repository, name) AS versions_count
FROM releases
ORDER BY repository, name, created DESC;

-- name: InsertReleasePromotion :exec
INSERT INTO release_promotions (id, release_id, environment, status, included_repositories, excluded_repositories, messages, created_by, created, created_millis)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: ListReleasePromotions :many
SELECT * FROM release_promotions WHERE release_id = $1 ORDER BY created DESC;

-- name: DeleteReleasePromotion :execrows
DELETE FROM release_promotions WHERE release_id = $1 AND created_millis = $2;
