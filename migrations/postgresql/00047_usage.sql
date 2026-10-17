-- Daily usage: per repository its files and bytes (a snapshot) and the
-- day's downloads and deploys; per user the day's downloads and deploys.
-- day is an ISO date (YYYY-MM-DD, UTC).

-- +goose Up
CREATE TABLE usage_daily (
    day       text   NOT NULL,
    repo_key  text   NOT NULL,
    files     bigint NOT NULL DEFAULT 0,
    bytes     bigint NOT NULL DEFAULT 0,
    downloads bigint NOT NULL DEFAULT 0,
    deploys   bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (day, repo_key)
);

CREATE TABLE usage_users_daily (
    day       text   NOT NULL,
    username  text   NOT NULL,
    downloads bigint NOT NULL DEFAULT 0,
    deploys   bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (day, username)
);

-- +goose Down
DROP TABLE usage_users_daily;
DROP TABLE usage_daily;
