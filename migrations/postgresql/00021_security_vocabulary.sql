-- +goose Up
ALTER TABLE users RENAME COLUMN profile TO extensions;
ALTER TABLE groups RENAME COLUMN auto_join TO default_for_new_users;
ALTER TABLE groups RENAME COLUMN admin_privileges TO admin;
ALTER TABLE permission_targets RENAME TO permissions;
ALTER TABLE access_tokens RENAME COLUMN scope TO scope_kind;
ALTER TABLE access_tokens ADD COLUMN scope_groups text[] NOT NULL DEFAULT '{}';
ALTER TABLE access_tokens RENAME COLUMN project_key TO project;

-- +goose Down
ALTER TABLE access_tokens RENAME COLUMN project TO project_key;
ALTER TABLE access_tokens DROP COLUMN scope_groups;
ALTER TABLE access_tokens RENAME COLUMN scope_kind TO scope;
ALTER TABLE permissions RENAME TO permission_targets;
ALTER TABLE groups RENAME COLUMN admin TO admin_privileges;
ALTER TABLE groups RENAME COLUMN default_for_new_users TO auto_join;
ALTER TABLE users RENAME COLUMN extensions TO profile;
