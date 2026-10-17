-- name: RegisterFindingCVEs :execrows
INSERT INTO cves (id)
SELECT DISTINCT upper(c) FROM (
    SELECT unnest(f.cves || f.vuln_id) AS c FROM scan_findings f
) s
WHERE c ILIKE 'CVE-%'
ON CONFLICT (id) DO NOTHING;

-- name: KnownCVEIDs :many
SELECT id FROM cves ORDER BY id;

-- name: SetCVEEPSS :exec
UPDATE cves c SET epss = v.score, epss_percentile = v.percentile, epss_date = @score_date
FROM (SELECT unnest(@ids::text[]) AS id, unnest(@scores::float8[]) AS score, unnest(@percentiles::float8[]) AS percentile) v
WHERE c.id = v.id;

-- name: SetCVEKEV :exec
WITH entries AS (
    SELECT unnest(@ids::text[]) AS id, unnest(@added::date[]) AS added, unnest(@due::date[]) AS due,
           unnest(@ransomware::bool[]) AS ransomware, unnest(@actions::text[]) AS action
), cleared AS (
    UPDATE cves SET kev_added = NULL, kev_due = NULL, kev_ransomware = false, kev_action = ''
    WHERE kev_added IS NOT NULL AND NOT (id = ANY(coalesce(@ids::text[], '{}')))
)
UPDATE cves c SET kev_added = e.added, kev_due = e.due, kev_ransomware = e.ransomware, kev_action = e.action
FROM entries e WHERE c.id = e.id;

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
UPDATE cves SET record_checked = @checked WHERE id = ANY(@ids::text[]);

-- name: GetCVEFeed :one
SELECT * FROM cve_feeds WHERE name = @name;

-- name: SaveCVEFeed :exec
INSERT INTO cve_feeds (name, version, fetched) VALUES (@name, @version, @fetched)
ON CONFLICT (name) DO UPDATE SET version = EXCLUDED.version, fetched = EXCLUDED.fetched;

-- name: ListCVEPage :many
-- Sort keys: epss (default), cvss, published, affected, id.
WITH affected AS (
    SELECT c.id,
           count(DISTINCT f.sha256)  AS artifacts,
           count(DISTINCT f.purl)    AS packages,
           min(CASE f.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END)::int AS severity_rank
    FROM cves c
    JOIN scan_findings f ON f.vuln_id = c.id OR c.id = ANY(f.cves)
    WHERE EXISTS (SELECT 1 FROM items i WHERE i.sha256 = f.sha256 AND i.type = 'file')
    GROUP BY c.id
)
SELECT c.id, c.title, c.published, c.cvss_score, c.cvss_version, c.epss, c.epss_percentile, c.kev_added, c.kev_ransomware,
       c.ssvc_exploitation, a.artifacts, a.packages, a.severity_rank
FROM cves c JOIN affected a ON a.id = c.id
WHERE (@query::text = '' OR strpos(lower(c.id), lower(@query)) > 0 OR strpos(lower(c.title), lower(@query)) > 0)
  AND (NOT @kev_only::bool OR c.kev_added IS NOT NULL)
  AND (@min_epss::float8 <= 0 OR c.epss >= @min_epss)
  AND (@severity_rank::int < 0 OR a.severity_rank = @severity_rank)
ORDER BY
  CASE WHEN @sort::text = 'cvss' THEN c.cvss_score END DESC NULLS LAST,
  CASE WHEN @sort::text = 'published' THEN c.published END DESC NULLS LAST,
  CASE WHEN @sort::text = 'affected' THEN a.artifacts END DESC,
  CASE WHEN @sort::text = 'id' THEN c.id END DESC,
  CASE WHEN @sort::text NOT IN ('cvss', 'published', 'affected', 'id') THEN (c.kev_added IS NOT NULL) END DESC,
  CASE WHEN @sort::text NOT IN ('cvss', 'published', 'affected', 'id') THEN c.epss END DESC NULLS LAST,
  a.severity_rank, c.id
LIMIT @max_rows OFFSET @skip;

-- name: CountCVEs :one
SELECT count(*) FROM cves c
JOIN LATERAL (
    SELECT min(CASE f.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END)::int AS severity_rank
    FROM scan_findings f
    WHERE (f.vuln_id = c.id OR c.id = ANY(f.cves))
      AND EXISTS (SELECT 1 FROM items i WHERE i.sha256 = f.sha256 AND i.type = 'file')
) a ON a.severity_rank IS NOT NULL
WHERE (@query::text = '' OR strpos(lower(c.id), lower(@query)) > 0 OR strpos(lower(c.title), lower(@query)) > 0)
  AND (NOT @kev_only::bool OR c.kev_added IS NOT NULL)
  AND (@min_epss::float8 <= 0 OR c.epss >= @min_epss)
  AND (@severity_rank::int < 0 OR a.severity_rank = @severity_rank);

-- name: GetCVE :one
SELECT * FROM cves WHERE id = @id;

-- name: CVEFindings :many
SELECT f.vuln_id, f.severity, f.package_name, f.package_version, f.purl, f.fixed_versions, f.sha256,
       i.repo_key, i.path, i.name
FROM scan_findings f
JOIN LATERAL (SELECT repo_key, path, name FROM items WHERE sha256 = f.sha256 AND type = 'file' ORDER BY repo_key, path, name LIMIT 1) i ON true
WHERE f.vuln_id = @id OR @id = ANY(f.cves)
ORDER BY i.repo_key, i.path, i.name, f.package_name
LIMIT 500;

-- name: CVEFactsFor :many
SELECT id, epss, epss_percentile, kev_added FROM cves WHERE id = ANY(@ids::text[]);
