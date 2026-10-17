-- An issue is one per resource: the same rule can fail at one file and line
-- for several resources (Terraform count or for_each, a model's imports).

-- +goose Up
ALTER TABLE scan_issues DROP CONSTRAINT scan_issues_pkey;
ALTER TABLE scan_issues ADD PRIMARY KEY (sha256, analyzer, rule, file, line, resource);

-- +goose Down
DELETE FROM scan_issues a USING scan_issues b
WHERE a.sha256 = b.sha256 AND a.analyzer = b.analyzer AND a.rule = b.rule AND a.file = b.file
  AND a.line = b.line AND a.resource > b.resource;
ALTER TABLE scan_issues DROP CONSTRAINT scan_issues_pkey;
ALTER TABLE scan_issues ADD PRIMARY KEY (sha256, analyzer, rule, file, line);
