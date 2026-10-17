-- Whether a secret found in content is still accepted by its provider:
-- live, inactive, unknown or error; empty when it was not checked. The
-- secret itself is never stored.

-- +goose Up
ALTER TABLE scan_issues ADD COLUMN validation text NOT NULL DEFAULT '';
ALTER TABLE scan_issues ADD COLUMN validated timestamptz;

-- +goose Down
ALTER TABLE scan_issues DROP COLUMN validated;
ALTER TABLE scan_issues DROP COLUMN validation;
