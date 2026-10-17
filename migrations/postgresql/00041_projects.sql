-- Projects: a key, members in roles, and a storage quota over the
-- repositories whose project is the key.

-- +goose Up
CREATE TABLE projects (
    key     text NOT NULL PRIMARY KEY,
    config  jsonb NOT NULL,
    created timestamptz NOT NULL,
    updated timestamptz NOT NULL
);

-- +goose Down
DROP TABLE projects;
