-- +goose Up
ALTER TABLE replications
    ADD COLUMN last_run    timestamptz,
    ADD COLUMN last_status text NOT NULL DEFAULT 'never_run',
    ADD COLUMN last_error  text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE replications DROP COLUMN last_error, DROP COLUMN last_status, DROP COLUMN last_run;
