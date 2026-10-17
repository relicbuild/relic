-- name: ListGroups :many
SELECT * FROM groups ORDER BY name;

-- name: GetGroup :one
SELECT * FROM groups WHERE name = @name;

-- name: UpsertGroup :one
-- Returns true when the group was created, false when replaced.
WITH prior AS MATERIALIZED (SELECT count(*) AS n FROM groups WHERE name = @name)
INSERT INTO groups (name, description, default_for_new_users, admin, viewer)
SELECT @name, @description, @default_for_new_users, @admin, @viewer FROM prior WHERE true
ON CONFLICT (name) DO UPDATE
SET description = excluded.description, default_for_new_users = excluded.default_for_new_users, admin = excluded.admin, viewer = excluded.viewer
RETURNING (SELECT n = 0 FROM prior) AS created;

-- name: DeleteGroup :execrows
DELETE FROM groups WHERE name = @name;

-- name: GroupMembers :many
SELECT username FROM group_members WHERE group_name = @group_name ORDER BY username;

-- name: ClearGroupMembers :exec
DELETE FROM group_members WHERE group_name = @group_name;

-- name: ListPermissions :many
SELECT * FROM permissions ORDER BY name;

-- name: GetPermission :one
SELECT * FROM permissions WHERE name = @name;

-- name: InsertPermission :exec
INSERT INTO permissions (name, config) VALUES (@name, CAST(@config AS TEXT));

-- name: UpdatePermission :execrows
UPDATE permissions SET config = CAST(@config AS TEXT), updated = strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now') WHERE name = @name;

-- name: DeletePermission :execrows
DELETE FROM permissions WHERE name = @name;
