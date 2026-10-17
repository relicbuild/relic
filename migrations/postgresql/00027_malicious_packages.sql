-- +goose Up
CREATE TABLE malicious_packages (
    id           text        NOT NULL,
    ecosystem    text        NOT NULL,
    name         text        NOT NULL,
    all_versions boolean     NOT NULL,
    versions     text[]      NOT NULL,
    ranges       jsonb       NOT NULL,
    summary      text        NOT NULL,
    modified     timestamptz NOT NULL,
    PRIMARY KEY (ecosystem, name, id)
);

CREATE TABLE malicious_feeds (
    ecosystem text        PRIMARY KEY,
    etag      text        NOT NULL,
    fetched   timestamptz NOT NULL,
    changed   timestamptz NOT NULL
);

ALTER TABLE scan_artifacts ADD COLUMN malicious_checked timestamptz;

-- +goose Down
ALTER TABLE scan_artifacts DROP COLUMN malicious_checked;
DROP TABLE malicious_feeds;
DROP TABLE malicious_packages;
