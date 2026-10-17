-- Workload identities exchange a CI job's OIDC token for a short-lived Relic token.

-- +goose Up
CREATE TABLE workload_identities (
    name    text NOT NULL PRIMARY KEY,
    config  text NOT NULL,
    created timestamp NOT NULL,
    updated timestamp NOT NULL
);

-- +goose Down
DROP TABLE workload_identities;
