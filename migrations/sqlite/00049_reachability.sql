-- What govulncheck found in a scanned artifact's Go binaries: per OSV entry
-- and module, whether a call path reaches the vulnerable code, with the
-- entry's aliases (CVE and GHSA IDs) to join Grype's findings, and a trace
-- of the call path when there is one.

-- +goose Up
CREATE TABLE scan_reachability (
    sha256    text    NOT NULL REFERENCES scan_artifacts (sha256) ON DELETE CASCADE,
    osv_id    text    NOT NULL,
    module    text    NOT NULL,
    aliases   text    NOT NULL,
    reachable boolean NOT NULL,
    trace     text    NOT NULL,
    PRIMARY KEY (sha256, osv_id, module)
);

-- +goose Down
DROP TABLE scan_reachability;
