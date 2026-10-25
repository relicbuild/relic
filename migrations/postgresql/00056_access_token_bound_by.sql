-- +goose Up
ALTER TABLE access_tokens ADD COLUMN bound_by text NOT NULL DEFAULT '';
ALTER TABLE access_tokens ADD COLUMN sign_in boolean NOT NULL DEFAULT false;
CREATE INDEX access_tokens_bound_by ON access_tokens (bound_by);

-- +goose Down
DROP INDEX access_tokens_bound_by;
ALTER TABLE access_tokens DROP COLUMN sign_in;
ALTER TABLE access_tokens DROP COLUMN bound_by;
