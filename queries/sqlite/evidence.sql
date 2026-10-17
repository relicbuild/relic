-- name: CreateTrustedKey :exec
INSERT INTO trusted_keys (kid, alias, key_type, public_key, fingerprint, issued_by, issued)
VALUES (@kid, @alias, @key_type, @public_key, @fingerprint, @issued_by, @issued);

-- name: GetTrustedKeyByAlias :one
SELECT * FROM trusted_keys WHERE alias = @alias;

-- name: ListTrustedKeys :many
SELECT * FROM trusted_keys ORDER BY alias;

-- name: UnverifyEvidenceForKey :exec
UPDATE evidence SET verified = false WHERE key_alias IN (SELECT alias FROM trusted_keys WHERE kid = @kid);

-- name: DeleteTrustedKey :execrows
DELETE FROM trusted_keys WHERE kid = @kid;

-- name: CreateEvidence :exec
INSERT INTO evidence (id, subject_repo, subject_path, subject_name, subject_sha256, predicate_type, predicate_slug,
    predicate_category, predicate, provider_id, key_alias, verified, attachments, item_path, item_sha256, created_by, created)
VALUES (@id, @subject_repo, @subject_path, @subject_name, @subject_sha256, @predicate_type, @predicate_slug,
    @predicate_category, CAST(@predicate AS TEXT), @provider_id, @key_alias, @verified, CAST(@attachments AS TEXT),
    @item_path, @item_sha256, @created_by, @created);

-- name: ListEvidenceForSubject :many
SELECT * FROM evidence
WHERE subject_repo = @subject_repo AND subject_path = @subject_path AND subject_name = @subject_name
ORDER BY created DESC;
