-- Administrators' decisions on files a quarantined remote repository holds.

-- +goose Up
CREATE TABLE quarantine_decisions (
    repo_key   text NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    sha256     text NOT NULL,
    decision   text NOT NULL CHECK (decision IN ('released', 'rejected')),
    reason     text NOT NULL DEFAULT '',
    decided_by text NOT NULL,
    decided    timestamp NOT NULL,
    PRIMARY KEY (repo_key, sha256)
);

-- +goose Down
DROP TABLE quarantine_decisions;
