-- name: ListMaliciousPackages :many
-- The advisories naming packages of these ecosystems and names; callers
-- keep the ecosystem and name pairs they asked for.
SELECT * FROM malicious_packages
WHERE ecosystem IN (SELECT value FROM json_each(@ecosystems)) AND name IN (SELECT value FROM json_each(@names))
ORDER BY id;

-- name: UpsertMaliciousPackage :exec
INSERT INTO malicious_packages (id, ecosystem, name, all_versions, versions, ranges, summary, modified)
VALUES (@id, @ecosystem, @name, @all_versions, @versions, CAST(@ranges AS TEXT), @summary, @modified)
ON CONFLICT (ecosystem, name, id) DO UPDATE
SET all_versions = excluded.all_versions, versions = excluded.versions, ranges = excluded.ranges,
    summary = excluded.summary, modified = excluded.modified;

-- name: DeleteMaliciousAdvisory :exec
DELETE FROM malicious_packages WHERE id = @id;

-- name: CountMaliciousPackages :one
SELECT count(*) FROM malicious_packages;

-- name: GetMaliciousFeed :one
SELECT * FROM malicious_feeds WHERE ecosystem = @ecosystem;

-- name: SaveMaliciousFeed :exec
INSERT INTO malicious_feeds (ecosystem, etag, fetched, changed)
VALUES (@ecosystem, @etag, @fetched, @changed)
ON CONFLICT (ecosystem) DO UPDATE
SET etag = excluded.etag, fetched = excluded.fetched, changed = excluded.changed;

-- name: LatestMaliciousChange :one
-- SQLite has no -infinity; with no feeds this returns the earliest time Go
-- represents (time.Time's zero).
SELECT coalesce(max(changed), '0001-01-01 00:00:00+00:00') FROM malicious_feeds;

-- name: ListUncheckedScans :many
SELECT sha256, sbom FROM scan_artifacts
WHERE status IN ('done', 'partial') AND sbom IS NOT NULL
  AND (malicious_checked IS NULL OR malicious_checked < @changed)
ORDER BY sha256
LIMIT @max_rows;

-- name: SetMaliciousChecked :exec
UPDATE scan_artifacts SET malicious_checked = @checked WHERE sha256 = @sha256;

-- name: DeleteMaliciousFindings :exec
DELETE FROM scan_findings WHERE sha256 = @sha256 AND vuln_id LIKE 'MAL-%';
