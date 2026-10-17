-- +goose Up
ALTER TABLE users ADD COLUMN identity_provider text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN identity_provider;
