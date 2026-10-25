-- name: GetDebianReleaseDate :one
SELECT * FROM debian_release_dates WHERE repo_key = @repo_key AND distribution = @distribution;

-- name: PutDebianReleaseDate :exec
INSERT INTO debian_release_dates (repo_key, distribution, digest, date)
VALUES (@repo_key, @distribution, @digest, @date)
ON CONFLICT (repo_key, distribution) DO UPDATE SET digest = excluded.digest, date = excluded.date;
