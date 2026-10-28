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

-- name: UpsertMaliciousPackages :exec
-- The arrays come as JSON arrays, read in parallel by index; each of
-- @versions is a JSON array of versions and each of @ranges the ranges' JSON.
WITH given_ids AS MATERIALIZED (SELECT key, value FROM json_each(@ids)),
     given_ecosystems AS MATERIALIZED (SELECT key, value FROM json_each(@ecosystems)),
     given_names AS MATERIALIZED (SELECT key, value FROM json_each(@names)),
     given_all_versions AS MATERIALIZED (SELECT key, value FROM json_each(@all_versions)),
     given_versions AS MATERIALIZED (SELECT key, value FROM json_each(@versions)),
     given_ranges AS MATERIALIZED (SELECT key, value FROM json_each(@ranges)),
     given_summaries AS MATERIALIZED (SELECT key, value FROM json_each(@summaries)),
     given_modified AS MATERIALIZED (SELECT key, value FROM json_each(@modified))
INSERT INTO malicious_packages (id, ecosystem, name, all_versions, versions, ranges, summary, modified)
SELECT i.value, e.value, n.value, a.value, v.value, r.value, s.value, m.value
FROM given_ids AS i
JOIN given_ecosystems AS e ON e.key = i.key
JOIN given_names AS n ON n.key = i.key
JOIN given_all_versions AS a ON a.key = i.key
JOIN given_versions AS v ON v.key = i.key
JOIN given_ranges AS r ON r.key = i.key
JOIN given_summaries AS s ON s.key = i.key
JOIN given_modified AS m ON m.key = i.key
WHERE true
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
