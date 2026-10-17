-- Legal holds: items, or folders with their contents, that cannot be deleted, moved or overwritten.

-- +goose Up
CREATE TABLE item_holds (
    repo_key   text NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    path       text NOT NULL,
    reason     text NOT NULL,
    created_by text NOT NULL,
    created    timestamp NOT NULL,
    PRIMARY KEY (repo_key, path)
);

-- +goose Down
DROP TABLE item_holds;
