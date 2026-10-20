-- +goose Up
CREATE INDEX malicious_packages_id ON malicious_packages (id);

-- +goose Down
DROP INDEX malicious_packages_id;
