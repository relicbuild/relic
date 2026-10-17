-- +goose Up
CREATE TABLE users (
    id            uuid PRIMARY KEY,
    username      text        NOT NULL UNIQUE,
    email         text        NOT NULL DEFAULT '',
    password_hash text        NOT NULL,
    admin         boolean     NOT NULL DEFAULT false,
    created       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE repositories (
    key          text        PRIMARY KEY,
    rclass       text        NOT NULL CHECK (rclass IN ('local', 'remote', 'virtual', 'federated')),
    package_type text        NOT NULL,
    project_key  text        NOT NULL DEFAULT '',
    config       jsonb       NOT NULL DEFAULT '{}',
    created      timestamptz NOT NULL DEFAULT now(),
    updated      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE blobs (
    sha256             text        PRIMARY KEY,
    sha1               text        NOT NULL,
    md5                text        NOT NULL,
    size               bigint      NOT NULL,
    refcount           bigint      NOT NULL DEFAULT 0 CHECK (refcount >= 0),
    created            timestamptz NOT NULL DEFAULT now(),
    unreferenced_since timestamptz
);
CREATE INDEX blobs_sha1 ON blobs (sha1);
CREATE INDEX blobs_gc ON blobs (unreferenced_since) WHERE refcount = 0;

CREATE TABLE items (
    id          uuid        PRIMARY KEY,
    repo_key    text        NOT NULL REFERENCES repositories (key),
    path        text        NOT NULL,
    name        text        NOT NULL,
    type        text        NOT NULL CHECK (type IN ('file', 'folder')),
    size        bigint      NOT NULL DEFAULT 0,
    sha1        text,
    sha256      text        REFERENCES blobs (sha256),
    md5         text,
    created     timestamptz NOT NULL,
    created_by  text        NOT NULL,
    modified    timestamptz NOT NULL,
    modified_by text        NOT NULL,
    updated     timestamptz NOT NULL,
    UNIQUE (repo_key, path, name),
    CHECK ((type = 'file') = (sha256 IS NOT NULL))
);
CREATE INDEX items_repo_path_prefix ON items (repo_key, path text_pattern_ops);
CREATE INDEX items_name ON items (name);
CREATE INDEX items_sha1 ON items (sha1);
CREATE INDEX items_sha256 ON items (sha256);

CREATE TABLE item_properties (
    item_id uuid NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    key     text NOT NULL,
    value   text NOT NULL,
    PRIMARY KEY (item_id, key, value)
);
CREATE INDEX item_properties_key_value ON item_properties (key, value);

-- +goose Down
DROP TABLE item_properties;
DROP TABLE items;
DROP TABLE blobs;
DROP TABLE repositories;
DROP TABLE users;
