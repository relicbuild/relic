-- name: DeleteReachability :exec
DELETE FROM scan_reachability WHERE sha256 = @sha256;

-- name: InsertReachability :exec
INSERT INTO scan_reachability (sha256, osv_id, module, aliases, reachable, trace)
VALUES (@sha256, @osv_id, @module, @aliases, @reachable, @trace)
ON CONFLICT (sha256, osv_id, module) DO UPDATE
SET reachable = scan_reachability.reachable OR excluded.reachable,
    trace = CASE WHEN scan_reachability.trace = '' THEN excluded.trace ELSE scan_reachability.trace END;

-- name: ListReachability :many
SELECT * FROM scan_reachability WHERE sha256 IN (SELECT value FROM json_each(@sha256s)) ORDER BY sha256, osv_id, module;

-- name: ListNotReachableContents :many
SELECT DISTINCT r.sha256, a.file_name FROM scan_reachability r
JOIN scan_artifacts a ON a.sha256 = r.sha256
WHERE NOT r.reachable
ORDER BY r.sha256
LIMIT @max_rows;

-- name: DeleteGoExtracts :exec
DELETE FROM scan_go_extracts WHERE sha256 = @sha256;

-- name: InsertGoExtract :exec
INSERT INTO scan_go_extracts (sha256, executable, extract) VALUES (@sha256, @executable, @extract)
ON CONFLICT (sha256, executable) DO UPDATE SET extract = excluded.extract;

-- name: ListGoExtracts :many
SELECT * FROM scan_go_extracts WHERE sha256 = @sha256 ORDER BY executable;

-- name: ListReachabilityStaleScans :many
-- Scans with Go extracts whose reachability was last analyzed before the Go
-- vulnerability database changed.
SELECT a.sha256 FROM scan_artifacts a
WHERE EXISTS (SELECT 1 FROM scan_go_extracts e WHERE e.sha256 = a.sha256)
  AND (a.reachability_checked IS NULL OR a.reachability_checked < @changed)
ORDER BY a.sha256
LIMIT @max_rows;

-- name: SetReachabilityChecked :exec
UPDATE scan_artifacts SET reachability_checked = @checked WHERE sha256 = @sha256;
