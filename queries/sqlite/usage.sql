-- name: AddRepositoryUsage :exec
INSERT INTO usage_daily (day, repo_key, downloads, deploys) VALUES (@day, @repo_key, @downloads, @deploys)
ON CONFLICT (day, repo_key) DO UPDATE SET downloads = usage_daily.downloads + excluded.downloads,
    deploys = usage_daily.deploys + excluded.deploys;

-- name: AddUserUsage :exec
INSERT INTO usage_users_daily (day, username, downloads, deploys) VALUES (@day, @username, @downloads, @deploys)
ON CONFLICT (day, username) DO UPDATE SET downloads = usage_users_daily.downloads + excluded.downloads,
    deploys = usage_users_daily.deploys + excluded.deploys;

-- name: SnapshotRepositoryUsage :exec
INSERT INTO usage_daily (day, repo_key, files, bytes)
SELECT CAST(@day AS TEXT), r.key, count(i.id), CAST(coalesce(sum(i.size), 0) AS INTEGER)
FROM repositories r LEFT JOIN items i ON i.repo_key = r.key AND i.type = 'file'
WHERE true
GROUP BY r.key
ON CONFLICT (day, repo_key) DO UPDATE SET files = excluded.files, bytes = excluded.bytes;

-- name: ListRepositoryUsage :many
SELECT * FROM usage_daily WHERE day >= @from_day AND day <= @to_day ORDER BY day, repo_key;

-- name: ListUserUsage :many
SELECT username, CAST(sum(downloads) AS INTEGER) AS downloads, CAST(sum(deploys) AS INTEGER) AS deploys
FROM usage_users_daily WHERE day >= @from_day AND day <= @to_day
GROUP BY username;

-- name: PurgeUsage :exec
DELETE FROM usage_daily WHERE day < @before;

-- name: PurgeUserUsage :exec
DELETE FROM usage_users_daily WHERE day < @before;
