-- +goose Up
ALTER TABLE scan_policies RENAME COLUMN type TO kind;
ALTER TABLE scan_graphs ADD COLUMN build_name text, ADD COLUMN build_number text, ADD COLUMN project text;
CREATE INDEX scan_graphs_build ON scan_graphs (build_name, build_number, project) WHERE build_name IS NOT NULL;

-- +goose Down
DROP INDEX scan_graphs_build;
ALTER TABLE scan_graphs DROP COLUMN project, DROP COLUMN build_number, DROP COLUMN build_name;
ALTER TABLE scan_policies RENAME COLUMN kind TO type;
