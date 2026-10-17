-- Model scanning reports issues of analyzer model.

-- +goose Up
ALTER TABLE scan_issues DROP CONSTRAINT scan_issues_analyzer_check;
ALTER TABLE scan_issues ADD CONSTRAINT scan_issues_analyzer_check
    CHECK (analyzer IN ('secrets', 'misconfiguration', 'license', 'model'));

-- +goose Down
DELETE FROM scan_issues WHERE analyzer = 'model';
ALTER TABLE scan_issues DROP CONSTRAINT scan_issues_analyzer_check;
ALTER TABLE scan_issues ADD CONSTRAINT scan_issues_analyzer_check
    CHECK (analyzer IN ('secrets', 'misconfiguration', 'license'));
