-- +goose Up
CREATE TABLE scan_artifacts (
    sha256      text        PRIMARY KEY REFERENCES blobs (sha256) ON DELETE CASCADE,
    file_name   text        NOT NULL,
    status      text        NOT NULL CHECK (status IN ('queued', 'done', 'partial', 'failed', 'skipped')),
    reason      text        NOT NULL DEFAULT '',
    sbom        bytea,
    components  integer     NOT NULL DEFAULT 0,
    db_built    timestamptz,
    updated     timestamptz NOT NULL
);
CREATE INDEX scan_artifacts_status ON scan_artifacts (status);

CREATE TABLE scan_findings (
    sha256          text     NOT NULL REFERENCES scan_artifacts (sha256) ON DELETE CASCADE,
    vuln_id         text     NOT NULL,
    purl            text     NOT NULL,
    package_name    text     NOT NULL,
    package_version text     NOT NULL,
    severity        text     NOT NULL,
    summary         text     NOT NULL,
    cves            text[]   NOT NULL,
    fixed_versions  text[]   NOT NULL,
    cvss            jsonb    NOT NULL,
    urls            text[]   NOT NULL,
    PRIMARY KEY (sha256, vuln_id, purl)
);

CREATE TABLE scan_graphs (
    id       uuid        PRIMARY KEY,
    username text        NOT NULL,
    status   text        NOT NULL CHECK (status IN ('queued', 'done', 'failed')),
    request  jsonb       NOT NULL,
    result   jsonb,
    error    text        NOT NULL DEFAULT '',
    created  timestamptz NOT NULL,
    updated  timestamptz NOT NULL
);
CREATE INDEX scan_graphs_created ON scan_graphs (created);

-- +goose Down
DROP TABLE scan_graphs;
DROP TABLE scan_findings;
DROP TABLE scan_artifacts;
