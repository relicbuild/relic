-- +goose Up
ALTER TABLE access_tokens ADD COLUMN workload_identity text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE access_tokens DROP COLUMN workload_identity;
