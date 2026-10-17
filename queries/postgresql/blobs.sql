-- name: GetBlob :one
SELECT * FROM blobs WHERE sha256 = @sha256;

-- name: GetBlobBySHA1 :one
SELECT * FROM blobs WHERE sha1 = @sha1 ORDER BY created LIMIT 1;

-- name: AcquireBlob :exec
INSERT INTO blobs (sha256, sha1, md5, size, refcount)
VALUES (@sha256, @sha1, @md5, @size, 1)
ON CONFLICT (sha256) DO UPDATE
SET refcount = blobs.refcount + 1, unreferenced_since = NULL, deleting = false,
    sha1 = CASE WHEN blobs.sha1 = '' THEN EXCLUDED.sha1 ELSE blobs.sha1 END,
    md5 = CASE WHEN blobs.sha1 = '' THEN EXCLUDED.md5 ELSE blobs.md5 END,
    size = CASE WHEN blobs.sha1 = '' THEN EXCLUDED.size ELSE blobs.size END;

-- name: LockBlobs :many
-- Locks the existing rows in digest order, the one lock order for blobs.
SELECT sha256 FROM blobs
WHERE sha256 = ANY(@sha256s::text[])
ORDER BY sha256
FOR NO KEY UPDATE;

-- name: ReleaseBlobs :exec
-- Drops one reference per array element (duplicates count).
UPDATE blobs b
SET refcount = b.refcount - r.n,
    unreferenced_since = CASE WHEN b.refcount - r.n = 0 THEN now() END
FROM (SELECT s AS sha256, count(*) AS n FROM unnest(@sha256s::text[]) AS s GROUP BY s) r
WHERE b.sha256 = r.sha256;

-- name: MarkCollectableBlobs :many
-- Marks unreferenced blobs past the grace period as deleting; rows a failed
-- run left marked are marked again.
UPDATE blobs SET deleting = true
WHERE sha256 IN (SELECT b.sha256 FROM blobs b
                 WHERE b.refcount = 0 AND b.unreferenced_since < @cutoff::timestamptz
                 ORDER BY b.unreferenced_since
                 LIMIT @max
                 FOR UPDATE SKIP LOCKED)
RETURNING sha256;

-- name: BlobDeleting :one
SELECT deleting FROM blobs WHERE sha256 = @sha256;

-- name: DeleteDeletingBlobs :execrows
-- Deletes the rows still marked: a deploy that took one back cleared it.
DELETE FROM blobs WHERE sha256 = ANY(@sha256s::text[]) AND deleting AND refcount = 0;

-- name: BlobStats :one
SELECT count(*) AS blobs,
       coalesce(sum(size), 0)::bigint AS bytes,
       count(*) FILTER (WHERE refcount = 0) AS unreferenced_blobs,
       coalesce(sum(size) FILTER (WHERE refcount = 0), 0)::bigint AS unreferenced_bytes
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
VALUES (@sha256, '', '', @size, 0, now())
ON CONFLICT (sha256) DO NOTHING;
