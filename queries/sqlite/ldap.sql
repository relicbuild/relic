-- name: ListLdapServers :many
SELECT * FROM ldap_servers ORDER BY name;

-- name: GetLdapServer :one
SELECT * FROM ldap_servers WHERE name = @name;

-- name: UpsertLdapServer :exec
INSERT INTO ldap_servers (name, config, updated)
VALUES (@name, CAST(@config AS TEXT), strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now'))
ON CONFLICT (name) DO UPDATE SET config = excluded.config, updated = strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now');

-- name: DeleteLdapServer :execrows
DELETE FROM ldap_servers WHERE name = @name;

-- name: ListLdapGroupMappings :many
SELECT * FROM ldap_group_mappings ORDER BY name;

-- name: GetLdapGroupMapping :one
SELECT * FROM ldap_group_mappings WHERE name = @name;

-- name: UpsertLdapGroupMapping :exec
INSERT INTO ldap_group_mappings (name, config, updated)
VALUES (@name, CAST(@config AS TEXT), strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now'))
ON CONFLICT (name) DO UPDATE SET config = excluded.config, updated = strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now');

-- name: DeleteLdapGroupMapping :execrows
DELETE FROM ldap_group_mappings WHERE name = @name;

-- name: CreateLdapUser :exec
-- An LDAP user's password hash never verifies: it is not a valid hash.
INSERT INTO users (id, username, email, password_hash, admin, realm)
VALUES (@id, @username, @email, '!ldap', false, 'ldap')
ON CONFLICT (username) DO NOTHING;

-- name: SetUserEmail :exec
UPDATE users SET email = @email WHERE username = @username AND email <> @email;

-- name: ReplaceLdapMemberships :exec
-- The user's LDAP-managed memberships become exactly the existing groups
-- whose names match (case-insensitively; pass lower-cased names, as a JSON
-- array).
DELETE FROM group_members
WHERE username = @username AND source = 'ldap'
  AND lower(group_name) NOT IN (SELECT value FROM json_each(@group_names));
INSERT INTO group_members (group_name, username, source)
SELECT g.name, @username, 'ldap' FROM groups g WHERE lower(g.name) IN (SELECT value FROM json_each(@group_names))
ON CONFLICT (group_name, username) DO NOTHING;
