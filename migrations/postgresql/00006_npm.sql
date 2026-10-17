-- +goose Up
CREATE TABLE npm_versions (
    item_id   uuid        PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    repo_key  text        NOT NULL,
    name      text        NOT NULL,
    version   text        NOT NULL,
    manifest  jsonb       NOT NULL,
    integrity text        NOT NULL DEFAULT '',
    created   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repo_key, name, version)
);
CREATE INDEX npm_versions_name ON npm_versions (repo_key, name);

CREATE TABLE npm_dist_tags (
    repo_key text NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    name     text NOT NULL,
    tag      text NOT NULL,
    version  text NOT NULL,
    PRIMARY KEY (repo_key, name, tag)
);

-- +goose Down
DROP TABLE npm_dist_tags;
DROP TABLE npm_versions;
