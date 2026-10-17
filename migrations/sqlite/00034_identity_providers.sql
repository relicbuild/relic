-- Identity providers sign users in with single sign-on (OIDC or SAML); the
-- users and memberships they manage have the realm and source sso.
-- SQLite cannot alter a CHECK constraint: users and group_members are rebuilt
-- with foreign keys off, which only works outside a transaction.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;

BEGIN;

CREATE TABLE users_new (
    id text NOT NULL PRIMARY KEY,
    username text NOT NULL UNIQUE,
    email text NOT NULL DEFAULT '',
    password_hash text NOT NULL,
    admin boolean NOT NULL DEFAULT false,
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')),
    extensions text NOT NULL DEFAULT '{}',
    realm text NOT NULL DEFAULT 'internal' CHECK (realm IN ('internal', 'ldap', 'sso')),
    viewer boolean NOT NULL DEFAULT false,
    disabled boolean NOT NULL DEFAULT false
);

INSERT INTO users_new (id, username, email, password_hash, admin, created, extensions, realm, viewer)
SELECT id, username, email, password_hash, admin, created, extensions, realm, viewer FROM users;

DROP TABLE users;

ALTER TABLE users_new RENAME TO users;

CREATE TABLE group_members_new (
    group_name text NOT NULL REFERENCES groups (name) ON DELETE CASCADE,
    username text NOT NULL REFERENCES users (username) ON DELETE CASCADE,
    source text NOT NULL DEFAULT 'internal' CHECK (source IN ('internal', 'ldap', 'sso')),
    PRIMARY KEY (group_name, username)
);

INSERT INTO group_members_new (group_name, username, source)
SELECT group_name, username, source FROM group_members;

DROP TABLE group_members;

ALTER TABLE group_members_new RENAME TO group_members;

CREATE INDEX group_members_user ON group_members (username);

CREATE TABLE identity_providers (
    name text NOT NULL PRIMARY KEY,
    config text NOT NULL,
    created timestamp NOT NULL,
    updated timestamp NOT NULL
);

CREATE TABLE sso_requests (
    state text NOT NULL PRIMARY KEY,
    provider text NOT NULL REFERENCES identity_providers (name) ON DELETE CASCADE,
    verifier text NOT NULL,
    nonce text NOT NULL,
    redirect text NOT NULL,
    created timestamp NOT NULL
);

COMMIT;

PRAGMA foreign_keys = ON;

-- +goose Down
DELETE FROM group_members WHERE source = 'sso';
DELETE FROM users WHERE realm = 'sso';

PRAGMA foreign_keys = OFF;

BEGIN;

DROP TABLE sso_requests;
DROP TABLE identity_providers;

CREATE TABLE users_old (
    id text NOT NULL PRIMARY KEY,
    username text NOT NULL UNIQUE,
    email text NOT NULL DEFAULT '',
    password_hash text NOT NULL,
    admin boolean NOT NULL DEFAULT false,
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')),
    extensions text NOT NULL DEFAULT '{}',
    realm text NOT NULL DEFAULT 'internal' CHECK (realm IN ('internal', 'ldap')),
    viewer boolean NOT NULL DEFAULT false
);

INSERT INTO users_old (id, username, email, password_hash, admin, created, extensions, realm, viewer)
SELECT id, username, email, password_hash, admin, created, extensions, realm, viewer FROM users;

DROP TABLE users;

ALTER TABLE users_old RENAME TO users;

CREATE TABLE group_members_old (
    group_name text NOT NULL REFERENCES groups (name) ON DELETE CASCADE,
    username text NOT NULL REFERENCES users (username) ON DELETE CASCADE,
    source text NOT NULL DEFAULT 'internal' CHECK (source IN ('internal', 'ldap')),
    PRIMARY KEY (group_name, username)
);

INSERT INTO group_members_old (group_name, username, source)
SELECT group_name, username, source FROM group_members;

DROP TABLE group_members;

ALTER TABLE group_members_old RENAME TO group_members;

CREATE INDEX group_members_user ON group_members (username);

COMMIT;

PRAGMA foreign_keys = ON;
