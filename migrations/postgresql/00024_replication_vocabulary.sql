-- +goose Up
ALTER TABLE replications RENAME COLUMN repo_key TO repository;
ALTER TABLE replications ALTER COLUMN last_status SET DEFAULT 'never';

-- +goose Down
ALTER TABLE replications ALTER COLUMN last_status SET DEFAULT 'never_run';
ALTER TABLE replications RENAME COLUMN repository TO repo_key;
