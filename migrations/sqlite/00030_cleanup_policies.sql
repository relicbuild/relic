-- Cleanup policies: which items to move to the trash, and how their last run went.

-- +goose Up
CREATE TABLE cleanup_policies (
    name text NOT NULL PRIMARY KEY,
    doc text NOT NULL,
    created timestamp NOT NULL,
    updated timestamp NOT NULL,
    last_run timestamp,
    last_moved integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE cleanup_policies;
