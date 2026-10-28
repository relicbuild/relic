-- name: RegisterFindingCVEs :execrows
INSERT INTO cves (id)
SELECT DISTINCT upper(c) FROM (
    SELECT j.value AS c FROM scan_findings f, json_each(f.cves) j
    UNION ALL
    SELECT vuln_id AS c FROM scan_findings
) s
WHERE lower(c) LIKE 'cve-%'
ON CONFLICT (id) DO NOTHING;

-- name: RegisterCVEs :exec
INSERT INTO cves (id)
SELECT DISTINCT upper(j.value) FROM json_each(@ids) j
WHERE lower(j.value) LIKE 'cve-%'
ON CONFLICT (id) DO NOTHING;

-- name: KnownCVEIDs :many
SELECT id FROM cves ORDER BY id;

-- name: SetCVEEPSS :exec
-- The arrays come as JSON arrays, read in parallel by index.
WITH given_ids AS MATERIALIZED (SELECT key, value FROM json_each(@ids)),
     given_scores AS MATERIALIZED (SELECT key, value FROM json_each(@scores)),
     given_percentiles AS MATERIALIZED (SELECT key, value FROM json_each(@percentiles))
UPDATE cves SET epss = v.score, epss_percentile = v.percentile, epss_date = date(@score_date)
FROM (SELECT i.value AS id, s.value AS score, p.value AS percentile
      FROM given_ids AS i
      JOIN given_scores AS s ON s.key = i.key
      JOIN given_percentiles AS p ON p.key = i.key) AS v
WHERE cves.id = v.id;

-- name: SetCVEKEV :exec
WITH given_ids AS MATERIALIZED (SELECT key, value FROM json_each(@ids)),
given_added AS MATERIALIZED (SELECT key, value FROM json_each(@added)),
given_due AS MATERIALIZED (SELECT key, value FROM json_each(@due)),
given_ransomware AS MATERIALIZED (SELECT key, value FROM json_each(@ransomware)),
given_actions AS MATERIALIZED (SELECT key, value FROM json_each(@actions)),
entries AS (
    SELECT i.value AS id, date(a.value) AS added, date(d.value) AS due, r.value AS ransomware, x.value AS action
    FROM given_ids AS i
    JOIN given_added AS a ON a.key = i.key
    JOIN given_due AS d ON d.key = i.key
    JOIN given_ransomware AS r ON r.key = i.key
    JOIN given_actions AS x ON x.key = i.key
)
UPDATE cves SET
    kev_added = (SELECT e.added FROM entries e WHERE e.id = cves.id),
    kev_due = (SELECT e.due FROM entries e WHERE e.id = cves.id),
    kev_ransomware = coalesce((SELECT e.ransomware FROM entries e WHERE e.id = cves.id), false),
    kev_action = coalesce((SELECT e.action FROM entries e WHERE e.id = cves.id), '')
WHERE kev_added IS NOT NULL OR id IN (SELECT id FROM entries);

-- name: CVEsDueForRecord :many
SELECT id FROM cves
WHERE record_checked IS NULL OR record_checked < @checked_before
ORDER BY record_checked NULLS FIRST, id
LIMIT @max_rows;

-- name: SaveCVERecord :exec
UPDATE cves SET
    title = @title, description = @description, state = @state, published = @published, updated = @updated,
    cwes = @cwes, refs = @refs, cvss_score = @cvss_score, cvss_vector = @cvss_vector, cvss_version = @cvss_version,
    cvss_source = @cvss_source, ssvc_exploitation = @ssvc_exploitation, ssvc_automatable = @ssvc_automatable,
    ssvc_impact = @ssvc_impact, record_checked = @checked
WHERE id = @id;

-- name: MarkCVERecordChecked :exec
UPDATE cves SET record_checked = @checked WHERE id IN (SELECT value FROM json_each(@ids));

-- name: GetCVEFeed :one
SELECT * FROM cve_feeds WHERE name = @name;

-- name: SaveCVEFeed :exec
INSERT INTO cve_feeds (name, version, fetched) VALUES (@name, @version, @fetched)
ON CONFLICT (name) DO UPDATE SET version = excluded.version, fetched = excluded.fetched;

-- name: ListCVEPage :many
-- Sort keys: epss (default), cvss, published, affected, id.
WITH affected AS (
    SELECT c.id,
           count(DISTINCT f.sha256)  AS artifacts,
           count(DISTINCT f.purl)    AS packages,
           CAST(min(CASE f.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END) AS INTEGER) AS severity_rank
    FROM cves c
    JOIN scan_findings f ON f.vuln_id = c.id OR c.id IN (SELECT value FROM json_each(f.cves))
    WHERE EXISTS (SELECT 1 FROM items i WHERE i.sha256 = f.sha256 AND i.type = 'file')
    GROUP BY c.id
)
SELECT c.id, c.title, c.published, c.cvss_score, c.cvss_version, c.epss, c.epss_percentile, c.kev_added, c.kev_ransomware,
       c.ssvc_exploitation, a.artifacts, a.packages, a.severity_rank
FROM cves c JOIN affected a ON a.id = c.id
WHERE (@query = '' OR instr(lower(c.id), lower(@query)) > 0 OR instr(lower(c.title), lower(@query)) > 0)
  AND (NOT @kev_only OR c.kev_added IS NOT NULL)
  AND (@min_epss <= 0 OR c.epss >= @min_epss)
  AND (@severity_rank < 0 OR a.severity_rank = @severity_rank)
ORDER BY
  CASE WHEN @sort = 'cvss' THEN c.cvss_score END DESC NULLS LAST,
  CASE WHEN @sort = 'published' THEN c.published END DESC NULLS LAST,
  CASE WHEN @sort = 'affected' THEN a.artifacts END DESC NULLS FIRST,
  CASE WHEN @sort = 'id' THEN c.id END DESC NULLS FIRST,
  CASE WHEN @sort NOT IN ('cvss', 'published', 'affected', 'id') THEN (c.kev_added IS NOT NULL) END DESC NULLS FIRST,
  CASE WHEN @sort NOT IN ('cvss', 'published', 'affected', 'id') THEN c.epss END DESC NULLS LAST,
  a.severity_rank, c.id
LIMIT @max_rows OFFSET @skip;

-- name: CountCVEs :one
SELECT count(*) FROM (
    SELECT c.id, c.title, c.kev_added, c.epss,
           (SELECT CAST(min(CASE f.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END) AS INTEGER)
            FROM scan_findings f
            WHERE (f.vuln_id = c.id OR c.id IN (SELECT value FROM json_each(f.cves)))
              AND EXISTS (SELECT 1 FROM items i WHERE i.sha256 = f.sha256 AND i.type = 'file')) AS severity_rank
    FROM cves c
) a
WHERE a.severity_rank IS NOT NULL
  AND (@query = '' OR instr(lower(a.id), lower(@query)) > 0 OR instr(lower(a.title), lower(@query)) > 0)
  AND (NOT @kev_only OR a.kev_added IS NOT NULL)
  AND (@min_epss <= 0 OR a.epss >= @min_epss)
  AND (@severity_rank < 0 OR a.severity_rank = @severity_rank);

-- name: GetCVE :one
SELECT * FROM cves WHERE id = @id;

-- name: CVEFindings :many
SELECT f.vuln_id, f.severity, f.package_name, f.package_version, f.purl, f.fixed_versions, f.sha256,
       i.repo_key, i.path, i.name
FROM scan_findings f
JOIN items i ON i.id = (SELECT id FROM items WHERE sha256 = f.sha256 AND type = 'file' ORDER BY repo_key, path, name LIMIT 1)
WHERE f.vuln_id = @id OR @id IN (SELECT value FROM json_each(f.cves))
ORDER BY i.repo_key, i.path, i.name, f.package_name
LIMIT 500;

-- name: CVEFactsFor :many
SELECT id, epss, epss_percentile, kev_added FROM cves WHERE id IN (SELECT value FROM json_each(@ids));
