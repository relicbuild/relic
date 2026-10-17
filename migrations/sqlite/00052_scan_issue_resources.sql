-- An issue is one per resource: the same rule can fail at one file and line
-- for several resources (Terraform count or for_each, a model's imports).

-- +goose Up
CREATE TABLE scan_issues_by_resource (
    sha256 text NOT NULL REFERENCES scan_artifacts (sha256) ON DELETE CASCADE,
    analyzer text NOT NULL CHECK (analyzer IN ('secrets', 'misconfiguration', 'license', 'model')),
    rule text NOT NULL,
    severity text NOT NULL,
    title text NOT NULL,
    file text NOT NULL,
    line integer NOT NULL,
    resource text NOT NULL,
    validation text NOT NULL DEFAULT '',
    validated timestamp,
    PRIMARY KEY (sha256, analyzer, rule, file, line, resource)
);
INSERT INTO scan_issues_by_resource
SELECT sha256, analyzer, rule, severity, title, file, line, resource, validation, validated FROM scan_issues;
DROP TABLE scan_issues;
ALTER TABLE scan_issues_by_resource RENAME TO scan_issues;

-- +goose Down
CREATE TABLE scan_issues_by_line (
    sha256 text NOT NULL REFERENCES scan_artifacts (sha256) ON DELETE CASCADE,
    analyzer text NOT NULL CHECK (analyzer IN ('secrets', 'misconfiguration', 'license', 'model')),
    rule text NOT NULL,
    severity text NOT NULL,
    title text NOT NULL,
    file text NOT NULL,
    line integer NOT NULL,
    resource text NOT NULL,
    validation text NOT NULL DEFAULT '',
    validated timestamp,
    PRIMARY KEY (sha256, analyzer, rule, file, line)
);
INSERT OR IGNORE INTO scan_issues_by_line
SELECT sha256, analyzer, rule, severity, title, file, line, resource, validation, validated
FROM scan_issues ORDER BY resource;
DROP TABLE scan_issues;
ALTER TABLE scan_issues_by_line RENAME TO scan_issues;
