-- name: ClaimUnscanned :many
INSERT INTO scan_artifacts (sha256, file_name, status, updated)
SELECT DISTINCT ON (i.sha256) i.sha256, i.name, 'queued', @now
FROM items i
LEFT JOIN scan_artifacts s ON s.sha256 = i.sha256
WHERE i.type = 'file' AND i.name ~* @name_pattern::text
  AND (s.sha256 IS NULL OR (s.status = 'queued' AND s.reason LIKE 'waiting%' AND s.updated < @retry_before))
ORDER BY i.sha256
LIMIT @max_rows
ON CONFLICT (sha256) DO UPDATE SET reason = '', updated = EXCLUDED.updated
  WHERE scan_artifacts.status = 'queued' AND scan_artifacts.reason LIKE 'waiting%'
RETURNING sha256, file_name;

-- name: ForgetFailedScans :many
DELETE FROM scan_artifacts WHERE sha256 = ANY(@sha256s::text[]) AND status = 'failed'
RETURNING sha256;

-- name: ForgetFailedMatches :execrows
DELETE FROM scan_artifacts WHERE status = 'failed' AND reason LIKE 'match:%';

-- name: GetScanArtifact :one
SELECT * FROM scan_artifacts WHERE sha256 = @sha256;

-- name: FinishScanArtifact :exec
UPDATE scan_artifacts
SET status = @status, reason = @reason, sbom = @sbom, components = @components, db_built = @db_built, updated = @now
WHERE sha256 = @sha256;

-- name: FinishUnchangedScanArtifact :execrows
-- Records a scan's result unless the artifact changed since the scan read it
-- (an SBOM import, or a newer claim).
UPDATE scan_artifacts
SET status = @status, reason = @reason, sbom = @sbom, components = @components, db_built = @db_built, updated = @now
WHERE sha256 = @sha256 AND updated = @expected;

-- name: DeleteFindings :exec
DELETE FROM scan_findings WHERE sha256 = @sha256;

-- name: InsertFinding :exec
INSERT INTO scan_findings (sha256, vuln_id, purl, package_name, package_version, severity, summary, cves, fixed_versions, cvss, urls)
VALUES (@sha256, @vuln_id, @purl, @package_name, @package_version, @severity, @summary, @cves, @fixed_versions, @cvss, @urls)
ON CONFLICT DO NOTHING;

-- name: ListFindings :many
SELECT * FROM scan_findings WHERE sha256 = @sha256 ORDER BY severity, vuln_id, purl;

-- name: ListStaleScans :many
SELECT sha256 FROM scan_artifacts
WHERE status IN ('done', 'partial', 'queued') AND sbom IS NOT NULL AND (db_built IS NULL OR db_built < @db_built)
ORDER BY sha256
LIMIT @max_rows;

-- name: InsertGraphScan :exec
INSERT INTO scan_graphs (id, username, status, request, build_name, build_number, project, created, updated)
VALUES (@id, @username, 'queued', @request, sqlc.narg(build_name), sqlc.narg(build_number), sqlc.narg(project), @now, @now);

-- name: GetGraphScan :one
SELECT * FROM scan_graphs WHERE id = @id;

-- name: FinishGraphScan :exec
UPDATE scan_graphs SET status = @status, result = @result, error = @error, updated = @now WHERE id = @id;

-- name: DeleteOldGraphScans :exec
DELETE FROM scan_graphs WHERE created < @before;

-- name: GetLatestBuildGraphScan :one
SELECT * FROM scan_graphs
WHERE build_name = @build_name::text AND build_number = @build_number::text AND project = @project::text
ORDER BY created DESC
LIMIT 1;

-- name: ListFindingsForContents :many
SELECT * FROM scan_findings WHERE sha256 = ANY(@sha256s::text[]) ORDER BY vuln_id, purl;

-- name: ScanCounts :many
SELECT status, count(*) AS count FROM scan_artifacts GROUP BY status;

-- name: FindingCounts :many
SELECT f.severity, count(*) AS count FROM scan_findings f
WHERE EXISTS (SELECT 1 FROM items i WHERE i.sha256 = f.sha256 AND i.type = 'file')
GROUP BY f.severity;

-- name: LatestDBBuilt :one
SELECT max(db_built)::timestamptz FROM scan_artifacts;

-- name: ListFindingsPage :many
SELECT f.vuln_id, f.severity, f.package_name, f.package_version, f.summary, f.cves, f.fixed_versions, f.sha256,
       f.purl, i.repo_key, i.path, i.name
FROM scan_findings f
JOIN LATERAL (SELECT repo_key, path, name FROM items
              WHERE sha256 = f.sha256 AND type = 'file' AND (@repository::text = '' OR repo_key = @repository)
              ORDER BY repo_key, path, name LIMIT 1) i ON true
WHERE (@severity::text = '' OR f.severity = @severity)
  AND (@query::text = '' OR strpos(lower(f.vuln_id), lower(@query)) > 0 OR strpos(lower(f.package_name), lower(@query)) > 0
       OR @query = ANY(f.cves))
  AND (@purl_type::text = '' OR split_part(split_part(f.purl, '/', 1), ':', 2) = @purl_type)
  AND (NOT @fixable_only::bool OR cardinality(f.fixed_versions) > 0)
  AND (NOT @kev_only::bool OR EXISTS (SELECT 1 FROM cves c WHERE (c.id = f.vuln_id OR c.id = ANY(f.cves)) AND c.kev_added IS NOT NULL))
  AND (NOT @malicious_only::bool OR f.vuln_id LIKE 'MAL-%')
ORDER BY CASE f.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END,
         f.vuln_id, f.purl, f.sha256
LIMIT @max_rows OFFSET @skip;

-- name: CountFindings :one
SELECT count(*) FROM scan_findings f
WHERE EXISTS (SELECT 1 FROM items i WHERE i.sha256 = f.sha256 AND i.type = 'file' AND (@repository::text = '' OR i.repo_key = @repository))
  AND (@severity::text = '' OR f.severity = @severity)
  AND (@query::text = '' OR strpos(lower(f.vuln_id), lower(@query)) > 0 OR strpos(lower(f.package_name), lower(@query)) > 0
       OR @query = ANY(f.cves))
  AND (@purl_type::text = '' OR split_part(split_part(f.purl, '/', 1), ':', 2) = @purl_type)
  AND (NOT @fixable_only::bool OR cardinality(f.fixed_versions) > 0)
  AND (NOT @kev_only::bool OR EXISTS (SELECT 1 FROM cves c WHERE (c.id = f.vuln_id OR c.id = ANY(f.cves)) AND c.kev_added IS NOT NULL))
  AND (NOT @malicious_only::bool OR f.vuln_id LIKE 'MAL-%');

-- name: SeverityCountsForContents :many
SELECT sha256, severity, count(*) AS count
FROM scan_findings WHERE sha256 = ANY(@sha256s::text[])
GROUP BY sha256, severity;

-- name: ImageManifestsInFolders :many
SELECT path, sha256::text AS sha256
FROM items
WHERE repo_key = @repo_key AND path = ANY(@paths::text[]) AND name = 'manifest.json' AND type = 'file';

-- name: DeleteIssues :exec
DELETE FROM scan_issues WHERE sha256 = @sha256;

-- name: InsertIssue :exec
INSERT INTO scan_issues (sha256, analyzer, rule, severity, title, file, line, resource, validation, validated)
VALUES (@sha256, @analyzer, @rule, @severity, @title, @file, @line, @resource, @validation, sqlc.narg(validated))
ON CONFLICT DO NOTHING;

-- name: ListIssues :many
SELECT * FROM scan_issues WHERE sha256 = @sha256
ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END,
         analyzer, file, line, rule;

-- name: ImportScanSbom :exec
-- Replaces an artifact's SBOM with an imported one; matching it comes next,
-- and until then it counts as matched against no database (stale).
INSERT INTO scan_artifacts (sha256, file_name, status, reason, sbom, components, db_built, updated)
VALUES (@sha256, @file_name, 'queued', 'SBOM imported, not matched yet', @sbom, @components, NULL, @now)
ON CONFLICT (sha256) DO UPDATE
SET status = 'queued', reason = 'SBOM imported, not matched yet', sbom = EXCLUDED.sbom, components = EXCLUDED.components,
    db_built = NULL, updated = EXCLUDED.updated;

-- name: ListFindingsForPackage :many
SELECT * FROM scan_findings WHERE purl = @purl ORDER BY vuln_id, sha256 LIMIT 1000;
