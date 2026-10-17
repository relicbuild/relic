-- +goose Up
CREATE TABLE scan_ignore_rules (
    id         uuid        PRIMARY KEY,
    doc        jsonb       NOT NULL,
    expires_at timestamptz,
    created    timestamptz NOT NULL,
    created_by text        NOT NULL
);

-- +goose Down
DROP TABLE scan_ignore_rules;
