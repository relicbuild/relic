-- Findings are looked up by package URL (is a package version allowed).

-- +goose Up
CREATE INDEX scan_findings_purl ON scan_findings (purl);

-- +goose Down
DROP INDEX scan_findings_purl;
