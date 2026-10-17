-- name: RecordDownloads :exec
INSERT INTO download_stats (item_id, downloads, last_downloaded, last_downloaded_by)
SELECT u.item_id, u.downloads, u.last_downloaded, u.last_downloaded_by
FROM (
    -- Parallel JSON arrays, zipped by index (equal lengths).
    SELECT id.value AS item_id, c.value AS downloads, l.value AS last_downloaded, b.value AS last_downloaded_by
    FROM json_each(@item_ids) AS id
    JOIN json_each(@counts) AS c ON c.key = id.key
    JOIN json_each(@lasts) AS l ON l.key = id.key
    JOIN json_each(@bys) AS b ON b.key = id.key
) u
JOIN items i ON i.id = u.item_id
-- WHERE true lets SQLite parse the ON CONFLICT clause after a join.
WHERE true
ON CONFLICT (item_id) DO UPDATE SET
    downloads = download_stats.downloads + excluded.downloads,
    last_downloaded_by = CASE WHEN excluded.last_downloaded >= download_stats.last_downloaded
        THEN excluded.last_downloaded_by ELSE download_stats.last_downloaded_by END,
    last_downloaded = max(download_stats.last_downloaded, excluded.last_downloaded);

-- name: GetDownloadStats :one
SELECT * FROM download_stats WHERE item_id = @item_id;
