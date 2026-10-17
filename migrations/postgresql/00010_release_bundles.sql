-- +goose Up
CREATE TABLE release_bundles (
    id             uuid        PRIMARY KEY,
    project_key    text        NOT NULL DEFAULT '',
    repository_key text        NOT NULL,
    name           text        NOT NULL,
    version        text        NOT NULL,
    status         text        NOT NULL,
    draft          boolean     NOT NULL DEFAULT false,
    tag            text        NOT NULL DEFAULT '',
    artifacts      jsonb       NOT NULL DEFAULT '[]',
    created_by     text        NOT NULL,
    created        timestamptz NOT NULL,
    UNIQUE (repository_key, name, version)
);

CREATE TABLE release_bundle_promotions (
    id                       uuid        PRIMARY KEY,
    bundle_id                uuid        NOT NULL REFERENCES release_bundles (id) ON DELETE CASCADE,
    environment              text        NOT NULL,
    status                   text        NOT NULL,
    included_repository_keys text[]      NOT NULL DEFAULT '{}',
    excluded_repository_keys text[]      NOT NULL DEFAULT '{}',
    messages                 jsonb       NOT NULL DEFAULT '[]',
    created_by               text        NOT NULL,
    created                  timestamptz NOT NULL,
    created_millis           bigint      NOT NULL,
    UNIQUE (bundle_id, created_millis)
);

-- +goose Down
DROP TABLE release_bundle_promotions;
DROP TABLE release_bundles;
