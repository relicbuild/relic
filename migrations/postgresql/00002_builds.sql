-- +goose Up
CREATE TABLE builds (
    id          uuid        PRIMARY KEY,
    project_key text        NOT NULL DEFAULT '',
    name        text        NOT NULL,
    number      text        NOT NULL,
    started     timestamptz NOT NULL,
    started_ms  bigint      NOT NULL,
    info        json        NOT NULL,
    created     timestamptz NOT NULL DEFAULT now(),
    created_by  text        NOT NULL
);
CREATE INDEX builds_runs ON builds (project_key, name, started DESC);

CREATE TABLE build_artifacts (
    build_id  uuid NOT NULL REFERENCES builds (id) ON DELETE CASCADE,
    module_id text NOT NULL,
    name      text NOT NULL,
    type      text NOT NULL,
    path      text NOT NULL,
    sha1      text NOT NULL,
    sha256    text NOT NULL,
    md5       text NOT NULL
);
CREATE INDEX build_artifacts_build ON build_artifacts (build_id);
CREATE INDEX build_artifacts_sha1 ON build_artifacts (sha1);

CREATE TABLE build_dependencies (
    build_id  uuid NOT NULL REFERENCES builds (id) ON DELETE CASCADE,
    module_id text NOT NULL,
    dep_id    text NOT NULL,
    type      text NOT NULL,
    scopes    text NOT NULL,
    sha1      text NOT NULL,
    sha256    text NOT NULL,
    md5       text NOT NULL
);
CREATE INDEX build_dependencies_build ON build_dependencies (build_id);
CREATE INDEX build_dependencies_sha1 ON build_dependencies (sha1);

CREATE TABLE build_promotions (
    id         uuid        PRIMARY KEY,
    build_id   uuid        NOT NULL REFERENCES builds (id) ON DELETE CASCADE,
    status     text        NOT NULL,
    comment    text        NOT NULL,
    repository text        NOT NULL,
    user_name  text        NOT NULL,
    created    timestamptz NOT NULL
);
CREATE INDEX build_promotions_build ON build_promotions (build_id, created);

-- +goose Down
DROP TABLE build_promotions;
DROP TABLE build_dependencies;
DROP TABLE build_artifacts;
DROP TABLE builds;
