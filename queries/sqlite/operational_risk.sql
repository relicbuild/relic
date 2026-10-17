-- name: DeleteEndOfLifeData :exec
DELETE FROM end_of_life_products;

-- name: InsertEndOfLifeProduct :exec
INSERT INTO end_of_life_products (product, label) VALUES (@product, @label);

-- name: InsertEndOfLifeIdentifier :exec
INSERT INTO end_of_life_identifiers (purl, product) VALUES (@purl, @product)
ON CONFLICT (purl, product) DO NOTHING;

-- name: InsertEndOfLifeCycle :exec
INSERT INTO end_of_life_cycles (product, cycle, eol_from, is_eol, is_maintained, latest)
VALUES (@product, @cycle, @eol_from, @is_eol, @is_maintained, @latest)
ON CONFLICT (product, cycle) DO NOTHING;

-- name: ListEndOfLifeCycles :many
-- The release cycles of the products these package URLs (without a version)
-- name.
SELECT i.purl, p.product, p.label, c.cycle, c.eol_from, c.is_eol, c.is_maintained, c.latest
FROM end_of_life_identifiers i
JOIN end_of_life_products p ON p.product = i.product
JOIN end_of_life_cycles c ON c.product = i.product
WHERE i.purl IN (SELECT value FROM json_each(@purls))
ORDER BY i.purl, p.product, c.cycle;

-- name: ListEndOfLifeProductCycles :many
SELECT p.product, p.label, c.cycle, c.eol_from, c.is_eol, c.is_maintained, c.latest
FROM end_of_life_products p
JOIN end_of_life_cycles c ON c.product = p.product
WHERE p.product IN (SELECT value FROM json_each(@products))
ORDER BY p.product, c.cycle;

-- name: CountEndOfLifeProducts :one
SELECT count(*) FROM end_of_life_products;

-- name: GetEndOfLifeFeed :one
SELECT * FROM end_of_life_feeds WHERE source = @source;

-- name: SaveEndOfLifeFeed :exec
INSERT INTO end_of_life_feeds (source, etag, fetched, changed, products)
VALUES (@source, @etag, @fetched, @changed, @products)
ON CONFLICT (source) DO UPDATE
SET etag = excluded.etag, fetched = excluded.fetched, changed = excluded.changed, products = excluded.products;

-- name: LatestEndOfLifeFeed :one
SELECT * FROM end_of_life_feeds ORDER BY changed DESC LIMIT 1;

-- name: GetDepsDevVersion :one
SELECT * FROM deps_dev_versions WHERE system = @system AND name = @name AND version = @version;

-- name: SaveDepsDevVersion :exec
INSERT INTO deps_dev_versions (system, name, version, deprecated, reason, maintained, fetched)
VALUES (@system, @name, @version, @deprecated, @reason, @maintained, @fetched)
ON CONFLICT (system, name, version) DO UPDATE
SET deprecated = excluded.deprecated, reason = excluded.reason, maintained = excluded.maintained, fetched = excluded.fetched;

-- name: DeleteOperationalRisks :exec
DELETE FROM scan_operational_risks WHERE sha256 = @sha256;

-- name: InsertOperationalRisk :exec
INSERT INTO scan_operational_risks (sha256, purl, kind, severity, package_name, package_version, product, cycle,
                                    eol_from, detail)
VALUES (@sha256, @purl, @kind, @severity, @package_name, @package_version, @product, @cycle, @eol_from, @detail)
ON CONFLICT (sha256, purl, kind) DO NOTHING;

-- name: ListOperationalRisks :many
SELECT * FROM scan_operational_risks WHERE sha256 = @sha256
ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END,
         kind, purl;

-- name: ListOperationalRisksForContents :many
SELECT * FROM scan_operational_risks WHERE sha256 IN (SELECT value FROM json_each(@sha256s)) ORDER BY kind, purl;

-- name: ListOperationalRisksPage :many
SELECT r.kind, r.severity, r.package_name, r.package_version, r.purl, r.product, r.cycle, r.eol_from, r.detail,
       r.sha256, i.repo_key, i.path, i.name
FROM scan_operational_risks r
JOIN items i ON i.id = (SELECT id FROM items
                        WHERE sha256 = r.sha256 AND type = 'file' AND (@repository = '' OR repo_key = @repository)
                        ORDER BY repo_key, path, name LIMIT 1)
WHERE (@kind = '' OR r.kind = @kind)
  AND (@query = '' OR instr(lower(r.package_name), lower(@query)) > 0 OR instr(lower(r.product), lower(@query)) > 0)
ORDER BY CASE r.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END,
         r.kind, r.purl, r.sha256
LIMIT @max_rows OFFSET @skip;

-- name: CountOperationalRisks :one
SELECT count(*) FROM scan_operational_risks r
WHERE EXISTS (SELECT 1 FROM items i WHERE i.sha256 = r.sha256 AND i.type = 'file'
                                     AND (@repository = '' OR i.repo_key = @repository))
  AND (@kind = '' OR r.kind = @kind)
  AND (@query = '' OR instr(lower(r.package_name), lower(@query)) > 0 OR instr(lower(r.product), lower(@query)) > 0);

-- name: ListRiskUncheckedScans :many
SELECT sha256, sbom FROM scan_artifacts
WHERE status IN ('done', 'partial') AND sbom IS NOT NULL
  AND (risks_checked IS NULL OR risks_checked < @changed)
ORDER BY sha256
LIMIT @max_rows;

-- name: SetRisksChecked :exec
UPDATE scan_artifacts SET risks_checked = @checked WHERE sha256 = @sha256;
