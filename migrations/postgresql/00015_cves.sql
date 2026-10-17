-- +goose Up
CREATE TABLE cves (
    id                text PRIMARY KEY,
    title             text        NOT NULL DEFAULT '',
    description       text        NOT NULL DEFAULT '',
    state             text        NOT NULL DEFAULT '',
    published         timestamptz,
    updated           timestamptz,
    cwes              text[]      NOT NULL DEFAULT '{}',
    refs              text[]      NOT NULL DEFAULT '{}',
    cvss_score        double precision,
    cvss_vector       text        NOT NULL DEFAULT '',
    cvss_version      text        NOT NULL DEFAULT '',
    cvss_source       text        NOT NULL DEFAULT '',
    ssvc_exploitation text        NOT NULL DEFAULT '',
    ssvc_automatable  text        NOT NULL DEFAULT '',
    ssvc_impact       text        NOT NULL DEFAULT '',
    epss              double precision,
    epss_percentile   double precision,
    epss_date         date,
    kev_added         date,
    kev_due           date,
    kev_ransomware    boolean     NOT NULL DEFAULT false,
    kev_action        text        NOT NULL DEFAULT '',
    record_checked    timestamptz,
    created           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cves_record_due ON cves (record_checked NULLS FIRST);

CREATE TABLE cve_feeds (
    name    text PRIMARY KEY,
    version text        NOT NULL DEFAULT '',
    fetched timestamptz NOT NULL
);

CREATE INDEX scan_findings_cves ON scan_findings USING gin (cves);
CREATE INDEX scan_findings_vuln ON scan_findings (vuln_id);

-- +goose Down
DROP INDEX scan_findings_vuln;
DROP INDEX scan_findings_cves;
DROP TABLE cve_feeds;
DROP TABLE cves;
