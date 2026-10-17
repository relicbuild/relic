-- name: RecordDownloads :exec
INSERT INTO download_stats (item_id, downloads, last_downloaded, last_downloaded_by)
SELECT u.item_id, u.downloads, u.last_downloaded, u.last_downloaded_by
FROM (
    -- Parallel arrays, zipped (equal lengths).
    SELECT unnest(@item_ids::uuid[]) AS item_id, unnest(@counts::bigint[]) AS downloads,
           unnest(@lasts::timestamptz[]) AS last_downloaded, unnest(@bys::text[]) AS last_downloaded_by
) u
JOIN items i ON i.id = u.item_id
ON CONFLICT (item_id) DO UPDATE SET
    downloads = download_stats.downloads + EXCLUDED.downloads,
    last_downloaded_by = CASE WHEN EXCLUDED.last_downloaded >= download_stats.last_downloaded
        THEN EXCLUDED.last_downloaded_by ELSE download_stats.last_downloaded_by END,
    last_downloaded = GREATEST(download_stats.last_downloaded, EXCLUDED.last_downloaded);

-- name: GetDownloadStats :one
SELECT * FROM download_stats WHERE item_id = @item_id;
