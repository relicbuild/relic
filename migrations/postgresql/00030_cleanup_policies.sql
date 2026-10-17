-- Cleanup policies: which items to move to the trash, and how their last run went.

-- +goose Up
CREATE TABLE cleanup_policies (
    name       text        PRIMARY KEY,
    doc        jsonb       NOT NULL,
    created    timestamptz NOT NULL,
    updated    timestamptz NOT NULL,
    last_run   timestamptz,
    last_moved bigint      NOT NULL DEFAULT 0,
    last_error text        NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE cleanup_policies;
