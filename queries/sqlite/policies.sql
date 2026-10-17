-- name: UpsertScanPolicy :exec
INSERT INTO scan_policies (name, kind, doc, created, updated) VALUES (@name, @kind, CAST(@doc AS TEXT), @now, @now)
ON CONFLICT (name) DO UPDATE SET kind = excluded.kind, doc = excluded.doc, updated = excluded.updated;

-- name: InsertScanPolicy :execrows
INSERT INTO scan_policies (name, kind, doc, created, updated) VALUES (@name, @kind, CAST(@doc AS TEXT), @now, @now)
ON CONFLICT (name) DO NOTHING;

-- name: GetScanPolicy :one
SELECT * FROM scan_policies WHERE name = @name;

-- name: ListScanPolicies :many
SELECT * FROM scan_policies ORDER BY name;

-- name: DeleteScanPolicy :execrows
DELETE FROM scan_policies WHERE name = @name;

-- name: UpsertScanWatch :exec
INSERT INTO scan_watches (name, doc, created, updated) VALUES (@name, CAST(@doc AS TEXT), @now, @now)
ON CONFLICT (name) DO UPDATE SET doc = excluded.doc, updated = excluded.updated;

-- name: InsertScanWatch :execrows
INSERT INTO scan_watches (name, doc, created, updated) VALUES (@name, CAST(@doc AS TEXT), @now, @now)
ON CONFLICT (name) DO NOTHING;

-- name: GetScanWatch :one
SELECT * FROM scan_watches WHERE name = @name;

-- name: ListScanWatches :many
SELECT * FROM scan_watches ORDER BY name;

-- name: DeleteScanWatch :execrows
DELETE FROM scan_watches WHERE name = @name;
