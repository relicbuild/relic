-- +goose Up
ALTER TABLE ldap_settings RENAME TO ldap_directories;
ALTER TABLE ldap_directories RENAME COLUMN key TO name;
ALTER TABLE ldap_group_settings RENAME TO ldap_group_mappings;

-- +goose Down
ALTER TABLE ldap_group_mappings RENAME TO ldap_group_settings;
ALTER TABLE ldap_directories RENAME COLUMN name TO key;
ALTER TABLE ldap_directories RENAME TO ldap_settings;
