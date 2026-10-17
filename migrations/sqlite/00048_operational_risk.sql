-- End-of-life data (endoflife.date): products, the package URLs (without a
-- version) that name them, and their release cycles. eol_from is an ISO
-- date, empty when the product announces none.
-- Operational risks are what a scan's components carry: past or near end of
-- life, deprecated, unmaintained. deps_dev_versions caches deps.dev answers;
-- maintained is the OpenSSF Scorecard "Maintained" score (0-10), -1 unknown.

-- +goose Up
CREATE TABLE end_of_life_products (
    product text NOT NULL PRIMARY KEY,
    label   text NOT NULL
);

CREATE TABLE end_of_life_identifiers (
    purl    text NOT NULL,
    product text NOT NULL REFERENCES end_of_life_products (product) ON DELETE CASCADE,
    PRIMARY KEY (purl, product)
);

CREATE TABLE end_of_life_cycles (
    product       text    NOT NULL REFERENCES end_of_life_products (product) ON DELETE CASCADE,
    cycle         text    NOT NULL,
    eol_from      text    NOT NULL,
    is_eol        boolean NOT NULL,
    is_maintained boolean NOT NULL,
    latest        text    NOT NULL,
    PRIMARY KEY (product, cycle)
);

CREATE TABLE end_of_life_feeds (
    source   text      NOT NULL PRIMARY KEY,
    etag     text        NOT NULL,
    fetched  timestamp   NOT NULL,
    changed  timestamp   NOT NULL,
    products integer     NOT NULL
);

CREATE TABLE deps_dev_versions (
    system      text        NOT NULL,
    name        text        NOT NULL,
    version     text        NOT NULL,
    deprecated  boolean     NOT NULL,
    reason      text        NOT NULL,
    maintained  integer     NOT NULL,
    fetched     timestamp   NOT NULL,
    PRIMARY KEY (system, name, version)
);

CREATE TABLE scan_operational_risks (
    sha256          text NOT NULL REFERENCES scan_artifacts (sha256) ON DELETE CASCADE,
    purl            text NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('end_of_life', 'deprecated', 'unmaintained')),
    severity        text NOT NULL,
    package_name    text NOT NULL,
    package_version text NOT NULL,
    product         text NOT NULL,
    cycle           text NOT NULL,
    eol_from        text NOT NULL,
    detail          text NOT NULL,
    PRIMARY KEY (sha256, purl, kind)
);

ALTER TABLE scan_artifacts ADD COLUMN risks_checked timestamp  ;

-- +goose Down
ALTER TABLE scan_artifacts DROP COLUMN risks_checked;
DROP TABLE scan_operational_risks;
DROP TABLE deps_dev_versions;
DROP TABLE end_of_life_feeds;
DROP TABLE end_of_life_cycles;
DROP TABLE end_of_life_identifiers;
DROP TABLE end_of_life_products;
