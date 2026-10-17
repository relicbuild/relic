-- name: GetSigningKeys :many
SELECT * FROM signing_keys ORDER BY created DESC;

-- name: InsertSigningKey :exec
INSERT INTO signing_keys (kid, private_key) VALUES (@kid, @private_key) ON CONFLICT DO NOTHING;

-- name: UpdateSigningKey :exec
UPDATE signing_keys SET private_key = @private_key WHERE kid = @kid;

-- name: InsertAccessToken :exec
-- scope_groups arrives as a JSON array, which is how the column stores it.
INSERT INTO access_tokens (
    id, username, scope_kind, scope_groups, refresh_token_hash, expires, description, project, workload_identity
)
VALUES (
    @id, @username, @scope_kind, @scope_groups, @refresh_token_hash, @expires, @description, @project,
    @workload_identity
);

-- name: GetAccessToken :one
SELECT * FROM access_tokens WHERE id = @id;

-- name: GetAccessTokenByRefreshHash :one
SELECT * FROM access_tokens WHERE refresh_token_hash = @refresh_token_hash;

-- name: RevokeAccessToken :exec
UPDATE access_tokens SET revoked = true, refresh_token_hash = NULL WHERE id = @id;

-- name: RevokeUserAccessTokens :exec
UPDATE access_tokens SET revoked = true, refresh_token_hash = NULL WHERE username = @username;

-- name: InsertWebLoginSession :exec
INSERT INTO web_login_sessions (id) VALUES (@id) ON CONFLICT DO NOTHING;

-- name: GetWebLoginSession :one
SELECT * FROM web_login_sessions WHERE id = @id;

-- name: ApproveWebLoginSession :execrows
UPDATE web_login_sessions SET approved_by = @username WHERE id = @id AND approved_by IS NULL;

-- name: DeleteWebLoginSession :execrows
DELETE FROM web_login_sessions WHERE id = @id;

-- name: DeleteExpiredWebLoginSessions :exec
DELETE FROM web_login_sessions WHERE created < @before;

-- name: ListAccessTokens :many
SELECT * FROM access_tokens
WHERE (@username IS NULL OR username = @username)
  AND (@include_revoked OR NOT revoked)
ORDER BY created DESC;
