-- +goose Up
ALTER TABLE users ADD COLUMN profile jsonb NOT NULL DEFAULT '{}';

CREATE TABLE groups (
    name             text        PRIMARY KEY,
    description      text        NOT NULL DEFAULT '',
    auto_join        boolean     NOT NULL DEFAULT false,
    admin_privileges boolean     NOT NULL DEFAULT false,
    created          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE group_members (
    group_name text NOT NULL REFERENCES groups (name) ON DELETE CASCADE,
    username   text NOT NULL REFERENCES users (username) ON DELETE CASCADE,
    PRIMARY KEY (group_name, username)
);
CREATE INDEX group_members_user ON group_members (username);

CREATE TABLE permission_targets (
    name    text        PRIMARY KEY,
    config  jsonb       NOT NULL,
    created timestamptz NOT NULL DEFAULT now(),
    updated timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE web_login_sessions (
    id          text        PRIMARY KEY,
    created     timestamptz NOT NULL DEFAULT now(),
    approved_by text
);

CREATE TABLE replications (
    repo_key text        PRIMARY KEY REFERENCES repositories (key) ON DELETE CASCADE,
    config   jsonb       NOT NULL,
    updated  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE access_tokens
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN project_key text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE access_tokens DROP COLUMN project_key, DROP COLUMN description;
DROP TABLE replications;
DROP TABLE web_login_sessions;
DROP TABLE permission_targets;
DROP TABLE group_members;
DROP TABLE groups;
ALTER TABLE users DROP COLUMN profile;
