-- +goose Up
ALTER TABLE users ADD COLUMN realm text NOT NULL DEFAULT 'internal' CHECK (realm IN ('internal', 'ldap'));

ALTER TABLE group_members ADD COLUMN source text NOT NULL DEFAULT 'internal' CHECK (source IN ('internal', 'ldap'));

CREATE TABLE ldap_settings (
    key     text        PRIMARY KEY,
    config  jsonb       NOT NULL,
    updated timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ldap_group_settings (
    name    text        PRIMARY KEY,
    config  jsonb       NOT NULL,
    updated timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE ldap_group_settings;
DROP TABLE ldap_settings;
ALTER TABLE group_members DROP COLUMN source;
ALTER TABLE users DROP COLUMN realm;
