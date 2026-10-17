-- Never the secret itself: the rule, where, and what.

-- +goose Up
CREATE TABLE scan_issues (
    sha256   text    NOT NULL REFERENCES scan_artifacts (sha256) ON DELETE CASCADE,
    analyzer text    NOT NULL CHECK (analyzer IN ('secrets', 'misconfiguration', 'license')),
    rule     text    NOT NULL,
    severity text    NOT NULL,
    title    text    NOT NULL,
    file     text    NOT NULL,
    line     integer NOT NULL,
    resource text    NOT NULL,
    PRIMARY KEY (sha256, analyzer, rule, file, line)
);

-- +goose Down
DROP TABLE scan_issues;
