-- When the storage check last re-hashed a blob's object.

-- +goose Up
ALTER TABLE blobs ADD COLUMN verified_at timestamptz;

-- +goose Down
ALTER TABLE blobs DROP COLUMN verified_at;
