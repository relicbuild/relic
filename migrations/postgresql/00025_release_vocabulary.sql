-- +goose Up
ALTER TABLE builds RENAME COLUMN project_key TO project;

ALTER TABLE release_bundles RENAME TO releases;
ALTER TABLE releases RENAME COLUMN project_key TO project;
ALTER TABLE releases RENAME COLUMN repository_key TO repository;

ALTER TABLE release_bundle_promotions RENAME TO release_promotions;
ALTER TABLE release_promotions RENAME COLUMN bundle_id TO release_id;
ALTER TABLE release_promotions RENAME COLUMN included_repository_keys TO included_repositories;
ALTER TABLE release_promotions RENAME COLUMN excluded_repository_keys TO excluded_repositories;

-- +goose Down
ALTER TABLE release_promotions RENAME COLUMN excluded_repositories TO excluded_repository_keys;
ALTER TABLE release_promotions RENAME COLUMN included_repositories TO included_repository_keys;
ALTER TABLE release_promotions RENAME COLUMN release_id TO bundle_id;
ALTER TABLE release_promotions RENAME TO release_bundle_promotions;

ALTER TABLE releases RENAME COLUMN repository TO repository_key;
ALTER TABLE releases RENAME COLUMN project TO project_key;
ALTER TABLE releases RENAME TO release_bundles;

ALTER TABLE builds RENAME COLUMN project TO project_key;
