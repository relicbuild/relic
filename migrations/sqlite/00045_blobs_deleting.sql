-- Garbage collection marks the blobs it deletes, so it deletes their objects
-- with no transaction open, and a deploy that takes one back writes it again.

-- +goose Up
ALTER TABLE blobs ADD COLUMN deleting boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE blobs DROP COLUMN deleting;
