-- name: ListGroups :many
SELECT * FROM groups ORDER BY name;

-- name: GetGroup :one
SELECT * FROM groups WHERE name = @name;

-- name: UpsertGroup :one
INSERT INTO groups (name, description, default_for_new_users, admin, viewer)
VALUES (@name, @description, @default_for_new_users, @admin, @viewer)
ON CONFLICT (name) DO UPDATE
SET description = EXCLUDED.description, default_for_new_users = EXCLUDED.default_for_new_users, admin = EXCLUDED.admin, viewer = EXCLUDED.viewer
RETURNING (xmax = 0)::boolean AS created;

-- name: DeleteGroup :execrows
DELETE FROM groups WHERE name = @name;

-- name: GroupMembers :many
SELECT username FROM group_members WHERE group_name = @group_name ORDER BY username;

-- name: ClearInternalGroupMembers :exec
-- The members an administrator set; identity providers and LDAP keep theirs.
DELETE FROM group_members WHERE group_name = @group_name AND source = 'internal';

-- name: ListPermissions :many
SELECT * FROM permissions ORDER BY name;

-- name: GetPermission :one
SELECT * FROM permissions WHERE name = @name;

-- name: InsertPermission :exec
INSERT INTO permissions (name, config) VALUES (@name, @config);

-- name: UpdatePermission :execrows
UPDATE permissions SET config = @config, updated = now() WHERE name = @name;

-- name: DeletePermission :execrows
DELETE FROM permissions WHERE name = @name;
