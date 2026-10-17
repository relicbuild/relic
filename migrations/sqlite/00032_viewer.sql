-- Viewers read the administration pages without changing anything.

-- +goose Up
ALTER TABLE users ADD COLUMN viewer boolean NOT NULL DEFAULT false;
ALTER TABLE groups ADD COLUMN viewer boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE groups DROP COLUMN viewer;
ALTER TABLE users DROP COLUMN viewer;
