-- Identity providers sign users in with single sign-on (OIDC or SAML); the
-- users and memberships they manage have the realm and source sso.

-- +goose Up
CREATE TABLE identity_providers (
    name    text        PRIMARY KEY,
    config  jsonb       NOT NULL,
    created timestamptz NOT NULL,
    updated timestamptz NOT NULL
);

CREATE TABLE sso_requests (
    state    text        PRIMARY KEY,
    provider text        NOT NULL REFERENCES identity_providers (name) ON DELETE CASCADE,
    verifier text        NOT NULL,
    nonce    text        NOT NULL,
    redirect text        NOT NULL,
    created  timestamptz NOT NULL
);

ALTER TABLE users ADD COLUMN disabled boolean NOT NULL DEFAULT false;
ALTER TABLE users DROP CONSTRAINT users_realm_check;
ALTER TABLE users ADD CONSTRAINT users_realm_check CHECK (realm IN ('internal', 'ldap', 'sso'));
ALTER TABLE group_members DROP CONSTRAINT group_members_source_check;
ALTER TABLE group_members ADD CONSTRAINT group_members_source_check CHECK (source IN ('internal', 'ldap', 'sso'));

-- +goose Down
DELETE FROM group_members WHERE source = 'sso';
DELETE FROM users WHERE realm = 'sso';
ALTER TABLE group_members DROP CONSTRAINT group_members_source_check;
ALTER TABLE group_members ADD CONSTRAINT group_members_source_check CHECK (source IN ('internal', 'ldap'));
ALTER TABLE users DROP CONSTRAINT users_realm_check;
ALTER TABLE users ADD CONSTRAINT users_realm_check CHECK (realm IN ('internal', 'ldap'));
ALTER TABLE users DROP COLUMN disabled;
DROP TABLE sso_requests;
DROP TABLE identity_providers;
