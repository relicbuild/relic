-- +goose Up
CREATE TABLE scan_policies (
    name    text        PRIMARY KEY,
    type    text        NOT NULL,
    doc     jsonb       NOT NULL,
    created timestamptz NOT NULL,
    updated timestamptz NOT NULL
);

CREATE TABLE scan_watches (
    name    text        PRIMARY KEY,
    doc     jsonb       NOT NULL,
    created timestamptz NOT NULL,
    updated timestamptz NOT NULL
);

-- +goose Down
DROP TABLE scan_watches;
DROP TABLE scan_policies;
