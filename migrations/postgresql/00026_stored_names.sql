-- +goose Up
ALTER TABLE ldap_directories RENAME TO ldap_servers;

-- +goose Down
ALTER TABLE ldap_servers RENAME TO ldap_directories;
