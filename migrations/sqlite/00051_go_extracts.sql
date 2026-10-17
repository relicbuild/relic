-- govulncheck's extract of each Go executable a scan found (gzipped JSON:
-- its modules and symbols), so its reachability is analyzed again when the
-- Go vulnerability database changes, without the executable.
-- reachability_checked is when that last happened.

-- +goose Up
CREATE TABLE scan_go_extracts (
    sha256     text  NOT NULL REFERENCES scan_artifacts (sha256) ON DELETE CASCADE,
    executable text  NOT NULL,
    extract    blob  NOT NULL,
    PRIMARY KEY (sha256, executable)
);

ALTER TABLE scan_artifacts ADD COLUMN reachability_checked timestamp;

-- +goose Down
ALTER TABLE scan_artifacts DROP COLUMN reachability_checked;
DROP TABLE scan_go_extracts;
