-- Promotion gates: CEL conditions a release version must meet to be promoted
-- to an environment.

-- +goose Up
CREATE TABLE promotion_gates (
    name        text NOT NULL PRIMARY KEY,
    environment text NOT NULL,
    condition   text NOT NULL,
    description text NOT NULL DEFAULT '',
    created     timestamp NOT NULL,
    updated     timestamp NOT NULL
);
CREATE INDEX promotion_gates_environment ON promotion_gates (environment);

-- +goose Down
DROP TABLE promotion_gates;
