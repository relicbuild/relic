-- Model scanning reports issues of analyzer model.

-- +goose Up
CREATE TABLE scan_issues_widened (
    sha256 text NOT NULL REFERENCES scan_artifacts (sha256) ON DELETE CASCADE,
    analyzer text NOT NULL CHECK (analyzer IN ('secrets', 'misconfiguration', 'license', 'model')),
    rule text NOT NULL,
    severity text NOT NULL,
    title text NOT NULL,
    file text NOT NULL,
    line integer NOT NULL,
    resource text NOT NULL,
    PRIMARY KEY (sha256, analyzer, rule, file, line)
);
INSERT INTO scan_issues_widened SELECT sha256, analyzer, rule, severity, title, file, line, resource FROM scan_issues;
DROP TABLE scan_issues;
ALTER TABLE scan_issues_widened RENAME TO scan_issues;

-- +goose Down
CREATE TABLE scan_issues_narrowed (
    sha256 text NOT NULL REFERENCES scan_artifacts (sha256) ON DELETE CASCADE,
    analyzer text NOT NULL CHECK (analyzer IN ('secrets', 'misconfiguration', 'license')),
    rule text NOT NULL,
    severity text NOT NULL,
    title text NOT NULL,
    file text NOT NULL,
    line integer NOT NULL,
    resource text NOT NULL,
    PRIMARY KEY (sha256, analyzer, rule, file, line)
);
INSERT INTO scan_issues_narrowed SELECT sha256, analyzer, rule, severity, title, file, line, resource
FROM scan_issues WHERE analyzer <> 'model';
DROP TABLE scan_issues;
ALTER TABLE scan_issues_narrowed RENAME TO scan_issues;
