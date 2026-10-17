-- +goose Up
ALTER TABLE repositories RENAME COLUMN rclass TO class;
ALTER TABLE repositories RENAME COLUMN package_type TO format;
ALTER TABLE repositories RENAME COLUMN project_key TO project;
ALTER TABLE repositories DROP CONSTRAINT repositories_rclass_check;
ALTER TABLE repositories ADD CONSTRAINT repositories_class_check CHECK (class IN ('local', 'remote', 'virtual'));

-- +goose Down
ALTER TABLE repositories DROP CONSTRAINT repositories_class_check;
ALTER TABLE repositories ADD CONSTRAINT repositories_rclass_check CHECK (class IN ('local', 'remote', 'virtual', 'federated'));
ALTER TABLE repositories RENAME COLUMN project TO project_key;
ALTER TABLE repositories RENAME COLUMN format TO package_type;
ALTER TABLE repositories RENAME COLUMN class TO rclass;
