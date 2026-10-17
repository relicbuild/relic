-- name: UpsertIdentityProvider :exec
INSERT INTO identity_providers (name, config, created, updated) VALUES (@name, @config, @now, @now)
ON CONFLICT (name) DO UPDATE SET config = EXCLUDED.config, updated = EXCLUDED.updated;

-- name: GetIdentityProvider :one
SELECT * FROM identity_providers WHERE name = @name;

-- name: ListIdentityProviders :many
SELECT * FROM identity_providers ORDER BY name;

-- name: DeleteIdentityProvider :execrows
DELETE FROM identity_providers WHERE name = @name;

-- name: InsertSsoRequest :exec
INSERT INTO sso_requests (state, provider, verifier, nonce, redirect, created)
VALUES (@state, @provider, @verifier, @nonce, @redirect, @created);

-- name: TakeSsoRequest :one
-- Deletes the request so its state can be used once.
DELETE FROM sso_requests WHERE state = @state RETURNING *;

-- name: DeleteExpiredSsoRequests :exec
DELETE FROM sso_requests WHERE created < @before;

-- name: CreateSsoUser :execrows
-- A single sign-on user's password hash never verifies: it is not a valid hash.
INSERT INTO users (id, username, email, password_hash, admin, realm, identity_provider)
VALUES (@id, @username, @email, '!sso', false, 'sso', @identity_provider)
ON CONFLICT (username) DO NOTHING;

-- name: ClaimSsoUser :exec
UPDATE users SET identity_provider = @identity_provider
WHERE username = @username AND realm = 'sso' AND identity_provider = '';

-- name: ReleaseSsoUsers :exec
UPDATE users SET identity_provider = '' WHERE realm = 'sso' AND identity_provider = @identity_provider;

-- name: ReplaceSsoMemberships :exec
-- The user's identity-provider memberships become exactly the existing groups
-- whose names match (case-insensitively; pass lower-cased names).
WITH gone AS (
    DELETE FROM group_members m
    WHERE m.username = @username AND m.source = 'sso' AND NOT (lower(m.group_name) = ANY(@group_names::text[]))
)
INSERT INTO group_members (group_name, username, source)
SELECT g.name, @username, 'sso' FROM groups g WHERE lower(g.name) = ANY(@group_names::text[])
ON CONFLICT (group_name, username) DO NOTHING;

-- name: ListSsoUsers :many
SELECT * FROM users
WHERE realm = 'sso' AND (@username::text = '' OR lower(username) = lower(@username))
ORDER BY created, username
LIMIT @max_rows OFFSET @skip;

-- name: CountSsoUsers :one
SELECT count(*) FROM users WHERE realm = 'sso' AND (@username::text = '' OR lower(username) = lower(@username));

-- name: GetSsoUser :one
SELECT * FROM users WHERE realm = 'sso' AND id = @id;

-- name: SetUserDisabled :exec
UPDATE users SET disabled = @disabled WHERE username = @username;

-- name: ListGroupsPage :many
SELECT * FROM groups
WHERE (@name::text = '' OR lower(name) = lower(@name))
ORDER BY name
LIMIT @max_rows OFFSET @skip;

-- name: CountGroups :one
SELECT count(*) FROM groups WHERE (@name::text = '' OR lower(name) = lower(@name));

-- name: SsoGroupMembers :many
-- The members an identity provider manages, with their user IDs.
SELECT u.id, u.username FROM group_members m JOIN users u ON u.username = m.username
WHERE m.group_name = @group_name AND m.source = 'sso'
ORDER BY u.username;

-- name: AddSsoGroupMember :exec
INSERT INTO group_members (group_name, username, source) VALUES (@group_name, @username, 'sso')
ON CONFLICT (group_name, username) DO NOTHING;

-- name: RemoveSsoGroupMember :exec
DELETE FROM group_members WHERE group_name = @group_name AND username = @username AND source = 'sso';

-- name: ClearSsoGroupMembers :exec
DELETE FROM group_members WHERE group_name = @group_name AND source = 'sso';
