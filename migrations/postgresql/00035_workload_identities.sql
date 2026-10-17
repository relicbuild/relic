-- Workload identities exchange a CI job's OIDC token for a short-lived Relic token.

-- +goose Up
CREATE TABLE workload_identities (
    name    text PRIMARY KEY,
    config  jsonb NOT NULL,
    created timestamptz NOT NULL,
    updated timestamptz NOT NULL
);

-- +goose Down
DROP TABLE workload_identities;
