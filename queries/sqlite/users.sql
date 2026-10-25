-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: CreateUser :exec
INSERT INTO users (id, username, email, password_hash, admin)
VALUES (@id, @username, @email, @password_hash, @admin);

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = @username;

-- name: ListUsers :many
SELECT * FROM users ORDER BY username;

-- name: UpsertUser :one
-- Returns true when the user was created, false when replaced.
WITH prior AS MATERIALIZED (SELECT count(*) AS n FROM users WHERE username = @username)
INSERT INTO users (id, username, email, password_hash, admin, viewer, extensions)
SELECT @id, @username, @email, @password_hash, @admin, @viewer, CAST(@extensions AS TEXT) FROM prior WHERE true
ON CONFLICT (username) DO UPDATE
SET email = excluded.email, password_hash = excluded.password_hash, admin = excluded.admin, viewer = excluded.viewer, extensions = excluded.extensions
RETURNING (SELECT n = 0 FROM prior) AS created;

-- name: SetUserPassword :execrows
UPDATE users SET password_hash = @password_hash WHERE username = @username AND realm = 'internal';

-- name: DeleteUser :execrows
DELETE FROM users WHERE username = @username;

-- name: CountAdmins :one
SELECT count(*) FROM users WHERE admin;

-- name: UserGroups :many
SELECT g.name, g.admin, g.viewer FROM groups g JOIN group_members m ON m.group_name = g.name
WHERE m.username = @username ORDER BY g.name;

-- name: DefaultGroups :many
SELECT name FROM groups WHERE default_for_new_users ORDER BY name;

-- name: ClearUserGroups :exec
DELETE FROM group_members WHERE username = @username;

-- name: AddGroupMembers :exec
-- The usernames come as a JSON array. WHERE true keeps SQLite from reading
-- ON CONFLICT as a join constraint.
INSERT INTO group_members (group_name, username)
SELECT @group_name, value FROM json_each(@usernames) WHERE true ON CONFLICT DO NOTHING;
