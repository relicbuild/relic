-- name: GetBlob :one
SELECT * FROM blobs WHERE sha256 = @sha256;

-- name: GetBlobBySHA1 :one
SELECT * FROM blobs WHERE sha1 = @sha1 ORDER BY created LIMIT 1;

-- name: AcquireBlob :exec
INSERT INTO blobs (sha256, sha1, md5, size, refcount)
VALUES (@sha256, @sha1, @md5, @size, 1)
ON CONFLICT (sha256) DO UPDATE
SET refcount = blobs.refcount + 1, unreferenced_since = NULL, deleting = false,
    sha1 = CASE WHEN blobs.sha1 = '' THEN excluded.sha1 ELSE blobs.sha1 END,
    md5 = CASE WHEN blobs.sha1 = '' THEN excluded.md5 ELSE blobs.md5 END,
    size = CASE WHEN blobs.sha1 = '' THEN excluded.size ELSE blobs.size END;

-- name: LockBlobs :many
-- SQLite has one writer, so nothing to lock. The digests come as a JSON array.
SELECT sha256 FROM blobs
WHERE sha256 IN (SELECT value FROM json_each(CAST(@sha256s AS TEXT)))
ORDER BY sha256;

-- name: ReleaseBlobs :exec
-- Drops one reference per array element (duplicates count). The digests
-- come as a JSON array.
UPDATE blobs
SET refcount = blobs.refcount - r.n,
    unreferenced_since = CASE WHEN blobs.refcount - r.n = 0 THEN strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now') END
FROM (SELECT value AS sha256, count(*) AS n FROM json_each(CAST(@sha256s AS TEXT)) GROUP BY value) AS r
WHERE blobs.sha256 = r.sha256;

-- name: MarkCollectableBlobs :many
-- Marks unreferenced blobs past the grace period as deleting; rows a failed
-- run left marked are marked again.
UPDATE blobs SET deleting = true
WHERE sha256 IN (SELECT b.sha256 FROM blobs b
                 WHERE b.refcount = 0 AND b.unreferenced_since < @cutoff
                 ORDER BY b.unreferenced_since
                 LIMIT @max)
RETURNING sha256;

-- name: BlobDeleting :one
SELECT deleting FROM blobs WHERE sha256 = @sha256;

-- name: DeleteDeletingBlobs :execrows
-- Deletes the rows still marked: a deploy that took one back cleared it.
DELETE FROM blobs
WHERE sha256 IN (SELECT value FROM json_each(CAST(@sha256s AS TEXT))) AND deleting AND refcount = 0;

-- name: BlobStats :one
SELECT count(*) AS blobs,
       CAST(coalesce(sum(size), 0) AS INTEGER) AS bytes,
       count(*) FILTER (WHERE refcount = 0) AS unreferenced_blobs,
       CAST(coalesce(sum(size) FILTER (WHERE refcount = 0), 0) AS INTEGER) AS unreferenced_bytes
FROM blobs;

-- name: ListBlobsAfter :many
-- One page of blobs in digest order, for the storage check.
SELECT sha256, size, refcount FROM blobs
WHERE sha256 > @after
ORDER BY sha256
LIMIT @max;

-- name: ListBlobsToVerify :many
-- Referenced blobs, the longest unverified first.
SELECT sha256, size FROM blobs
WHERE refcount > 0
ORDER BY verified_at NULLS FIRST, sha256
LIMIT @max;

-- name: MarkBlobVerified :exec
UPDATE blobs SET verified_at = @verified_at WHERE sha256 = @sha256;

-- name: AdoptOrphanBlob :execrows
-- Records an object no blob row names as unreferenced, so garbage
-- collection removes it under its locks; empty checksums mark it adopted.
INSERT INTO blobs (sha256, sha1, md5, size, refcount, unreferenced_since)
VALUES (@sha256, '', '', @size, 0, strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now'))
ON CONFLICT (sha256) DO NOTHING;
