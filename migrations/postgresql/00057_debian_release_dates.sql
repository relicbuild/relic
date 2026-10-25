-- +goose Up
CREATE TABLE debian_release_dates (
    repo_key     text NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    distribution text NOT NULL,
    digest       text NOT NULL,
    date         timestamptz NOT NULL,
    PRIMARY KEY (repo_key, distribution)
);

-- +goose Down
DROP TABLE debian_release_dates;
