-- +goose Up
CREATE TABLE trusted_keys (
    kid         text        PRIMARY KEY,
    alias       text        NOT NULL UNIQUE,
    key_type    text        NOT NULL,
    public_key  text        NOT NULL,
    fingerprint text        NOT NULL,
    issued_by   text        NOT NULL,
    issued      timestamptz NOT NULL
);

CREATE TABLE evidence (
    id                 uuid        PRIMARY KEY,
    subject_repo       text        NOT NULL,
    subject_path       text        NOT NULL,
    subject_name       text        NOT NULL,
    subject_sha256     text        NOT NULL,
    predicate_type     text        NOT NULL,
    predicate_slug     text        NOT NULL,
    predicate_category text        NOT NULL,
    predicate          jsonb,
    provider_id        text        NOT NULL DEFAULT '',
    key_alias          text        NOT NULL DEFAULT '',
    verified           boolean     NOT NULL,
    attachments        jsonb       NOT NULL DEFAULT '[]',
    item_path          text        NOT NULL,
    item_sha256        text        NOT NULL,
    created_by         text        NOT NULL,
    created            timestamptz NOT NULL
);

CREATE INDEX evidence_subject ON evidence (subject_repo, subject_path, subject_name, created DESC);

-- +goose Down
DROP TABLE evidence;
DROP TABLE trusted_keys;
