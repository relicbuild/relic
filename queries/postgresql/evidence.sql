-- name: CreateTrustedKey :exec
INSERT INTO trusted_keys (kid, alias, key_type, public_key, fingerprint, issued_by, issued)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetTrustedKeyByAlias :one
SELECT * FROM trusted_keys WHERE alias = $1;

-- name: ListTrustedKeys :many
SELECT * FROM trusted_keys ORDER BY alias;

-- name: UnverifyEvidenceForKey :exec
UPDATE evidence SET verified = false WHERE key_alias IN (SELECT alias FROM trusted_keys WHERE kid = $1);

-- name: DeleteTrustedKey :execrows
DELETE FROM trusted_keys WHERE kid = $1;

-- name: CreateEvidence :exec
INSERT INTO evidence (id, subject_repo, subject_path, subject_name, subject_sha256, predicate_type, predicate_slug,
    predicate_category, predicate, provider_id, key_alias, verified, attachments, item_path, item_sha256, created_by, created)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17);

-- name: ListEvidenceForSubject :many
SELECT * FROM evidence WHERE subject_repo = $1 AND subject_path = $2 AND subject_name = $3 ORDER BY created DESC;
