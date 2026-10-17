-- Deletes in federated repositories, so a delete wins over an older write
-- when peers synchronize.

-- +goose Up
CREATE TABLE replication_tombstones (
    repo_key text NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    path     text NOT NULL,
    deleted  timestamp NOT NULL,
    PRIMARY KEY (repo_key, path)
);

-- +goose Down
DROP TABLE replication_tombstones;
