-- +goose Up
CREATE TABLE repositories (
    key text NOT NULL PRIMARY KEY,
    class text NOT NULL CHECK (class IN ('local', 'remote', 'virtual')),
    format text NOT NULL,
    project text NOT NULL DEFAULT '',
    config text NOT NULL DEFAULT '{}',
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')),
    updated timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now'))
);

CREATE TABLE blobs (
    sha256 text NOT NULL PRIMARY KEY,
    sha1 text NOT NULL,
    md5 text NOT NULL,
    size integer NOT NULL,
    refcount integer NOT NULL DEFAULT 0 CHECK (refcount >= 0),
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')),
    unreferenced_since timestamp
);

CREATE INDEX blobs_gc ON blobs (unreferenced_since) WHERE refcount = 0;
CREATE INDEX blobs_sha1 ON blobs (sha1);

CREATE TABLE items (
    id text NOT NULL PRIMARY KEY,
    repo_key text NOT NULL REFERENCES repositories (key),
    path text NOT NULL,
    name text NOT NULL,
    type text NOT NULL CHECK (type IN ('file', 'folder')),
    size integer NOT NULL DEFAULT 0,
    sha1 text,
    sha256 text REFERENCES blobs (sha256),
    md5 text,
    created timestamp NOT NULL,
    created_by text NOT NULL,
    modified timestamp NOT NULL,
    modified_by text NOT NULL,
    updated timestamp NOT NULL,
    UNIQUE (repo_key, path, name),
    CHECK ((type = 'file') = (sha256 IS NOT NULL))
);

CREATE INDEX items_name ON items (name);
CREATE INDEX items_repo_path_prefix ON items (repo_key, path);
CREATE INDEX items_sha1 ON items (sha1);
CREATE INDEX items_sha256 ON items (sha256);

CREATE TABLE item_properties (
    item_id text NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    key text NOT NULL,
    value text NOT NULL,
    PRIMARY KEY (item_id, key, value)
);

CREATE INDEX item_properties_key_value ON item_properties (key, value);

CREATE TABLE download_stats (
    item_id text NOT NULL PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    downloads integer NOT NULL,
    last_downloaded timestamp NOT NULL,
    last_downloaded_by text NOT NULL
);

CREATE TABLE npm_versions (
    item_id text NOT NULL PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    repo_key text NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    manifest text NOT NULL,
    integrity text NOT NULL DEFAULT '',
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')),
    UNIQUE (repo_key, name, version)
);

CREATE INDEX npm_versions_name ON npm_versions (repo_key, name);

CREATE TABLE npm_dist_tags (
    repo_key text NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    name text NOT NULL,
    tag text NOT NULL,
    version text NOT NULL,
    PRIMARY KEY (repo_key, name, tag)
);

CREATE TABLE replications (
    repository text NOT NULL PRIMARY KEY REFERENCES repositories (key) ON DELETE CASCADE,
    config text NOT NULL,
    updated timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')),
    last_run timestamp,
    last_status text NOT NULL DEFAULT 'never',
    last_error text NOT NULL DEFAULT ''
);

CREATE TABLE users (
    id text NOT NULL PRIMARY KEY,
    username text NOT NULL UNIQUE,
    email text NOT NULL DEFAULT '',
    password_hash text NOT NULL,
    admin boolean NOT NULL DEFAULT false,
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')),
    extensions text NOT NULL DEFAULT '{}',
    realm text NOT NULL DEFAULT 'internal' CHECK (realm IN ('internal', 'ldap'))
);

CREATE TABLE groups (
    name text NOT NULL PRIMARY KEY,
    description text NOT NULL DEFAULT '',
    default_for_new_users boolean NOT NULL DEFAULT false,
    admin boolean NOT NULL DEFAULT false,
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now'))
);

CREATE TABLE group_members (
    group_name text NOT NULL REFERENCES groups (name) ON DELETE CASCADE,
    username text NOT NULL REFERENCES users (username) ON DELETE CASCADE,
    source text NOT NULL DEFAULT 'internal' CHECK (source IN ('internal', 'ldap')),
    PRIMARY KEY (group_name, username)
);

CREATE INDEX group_members_user ON group_members (username);

CREATE TABLE permissions (
    name text NOT NULL PRIMARY KEY,
    config text NOT NULL,
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')),
    updated timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now'))
);

CREATE TABLE access_tokens (
    id text NOT NULL PRIMARY KEY,
    username text NOT NULL,
    scope_kind text NOT NULL,
    refresh_token_hash text UNIQUE,
    expires timestamp,
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')),
    revoked boolean NOT NULL DEFAULT false,
    description text NOT NULL DEFAULT '',
    project text NOT NULL DEFAULT '',
    scope_groups text NOT NULL DEFAULT '[]'
);

CREATE INDEX access_tokens_username ON access_tokens (username);

CREATE TABLE signing_keys (
    kid text NOT NULL PRIMARY KEY,
    private_key blob NOT NULL,
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now'))
);

CREATE TABLE trusted_keys (
    kid text NOT NULL PRIMARY KEY,
    alias text NOT NULL UNIQUE,
    key_type text NOT NULL,
    public_key text NOT NULL,
    fingerprint text NOT NULL,
    issued_by text NOT NULL,
    issued timestamp NOT NULL
);

CREATE TABLE key_pairs (
    name text NOT NULL PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('rsa', 'gpg')),
    private_key blob NOT NULL,
    public_key text NOT NULL,
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now'))
);

CREATE TABLE web_login_sessions (
    id text NOT NULL PRIMARY KEY,
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')),
    approved_by text
);

CREATE TABLE ldap_servers (
    name text NOT NULL PRIMARY KEY,
    config text NOT NULL,
    updated timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now'))
);

CREATE TABLE ldap_group_mappings (
    name text NOT NULL PRIMARY KEY,
    config text NOT NULL,
    updated timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now'))
);

CREATE TABLE builds (
    id text NOT NULL PRIMARY KEY,
    project text NOT NULL DEFAULT '',
    name text NOT NULL,
    number text NOT NULL,
    started timestamp NOT NULL,
    started_ms integer NOT NULL,
    info text NOT NULL,
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now')),
    created_by text NOT NULL
);

CREATE INDEX builds_runs ON builds (project, name, started DESC);

CREATE TABLE build_artifacts (
    build_id text NOT NULL REFERENCES builds (id) ON DELETE CASCADE,
    module_id text NOT NULL,
    name text NOT NULL,
    type text NOT NULL,
    path text NOT NULL,
    sha1 text NOT NULL,
    sha256 text NOT NULL,
    md5 text NOT NULL
);

CREATE INDEX build_artifacts_build ON build_artifacts (build_id);
CREATE INDEX build_artifacts_sha1 ON build_artifacts (sha1);

CREATE TABLE build_dependencies (
    build_id text NOT NULL REFERENCES builds (id) ON DELETE CASCADE,
    module_id text NOT NULL,
    dep_id text NOT NULL,
    type text NOT NULL,
    scopes text NOT NULL,
    sha1 text NOT NULL,
    sha256 text NOT NULL,
    md5 text NOT NULL
);

CREATE INDEX build_dependencies_build ON build_dependencies (build_id);
CREATE INDEX build_dependencies_sha1 ON build_dependencies (sha1);

CREATE TABLE build_promotions (
    id text NOT NULL PRIMARY KEY,
    build_id text NOT NULL REFERENCES builds (id) ON DELETE CASCADE,
    status text NOT NULL,
    comment text NOT NULL,
    repository text NOT NULL,
    user_name text NOT NULL,
    created timestamp NOT NULL
);

CREATE INDEX build_promotions_build ON build_promotions (build_id, created);

CREATE TABLE releases (
    id text NOT NULL PRIMARY KEY,
    project text NOT NULL DEFAULT '',
    repository text NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    status text NOT NULL,
    draft boolean NOT NULL DEFAULT false,
    tag text NOT NULL DEFAULT '',
    artifacts text NOT NULL DEFAULT '[]',
    created_by text NOT NULL,
    created timestamp NOT NULL,
    UNIQUE (repository, name, version)
);

CREATE TABLE release_promotions (
    id text NOT NULL PRIMARY KEY,
    release_id text NOT NULL REFERENCES releases (id) ON DELETE CASCADE,
    environment text NOT NULL,
    status text NOT NULL,
    included_repositories text NOT NULL DEFAULT '[]',
    excluded_repositories text NOT NULL DEFAULT '[]',
    messages text NOT NULL DEFAULT '[]',
    created_by text NOT NULL,
    created timestamp NOT NULL,
    created_millis integer NOT NULL,
    UNIQUE (release_id, created_millis)
);

CREATE TABLE evidence (
    id text NOT NULL PRIMARY KEY,
    subject_repo text NOT NULL,
    subject_path text NOT NULL,
    subject_name text NOT NULL,
    subject_sha256 text NOT NULL,
    predicate_type text NOT NULL,
    predicate_slug text NOT NULL,
    predicate_category text NOT NULL,
    predicate text,
    provider_id text NOT NULL DEFAULT '',
    key_alias text NOT NULL DEFAULT '',
    verified boolean NOT NULL,
    attachments text NOT NULL DEFAULT '[]',
    item_path text NOT NULL,
    item_sha256 text NOT NULL,
    created_by text NOT NULL,
    created timestamp NOT NULL
);

CREATE INDEX evidence_subject ON evidence (subject_repo, subject_path, subject_name, created DESC);

CREATE TABLE scan_artifacts (
    sha256 text NOT NULL PRIMARY KEY REFERENCES blobs (sha256) ON DELETE CASCADE,
    file_name text NOT NULL,
    status text NOT NULL CHECK (status IN ('queued', 'done', 'partial', 'failed', 'skipped')),
    reason text NOT NULL DEFAULT '',
    sbom blob,
    components integer NOT NULL DEFAULT 0,
    db_built timestamp,
    updated timestamp NOT NULL,
    malicious_checked timestamp
);

CREATE INDEX scan_artifacts_status ON scan_artifacts (status);

CREATE TABLE scan_findings (
    sha256 text NOT NULL REFERENCES scan_artifacts (sha256) ON DELETE CASCADE,
    vuln_id text NOT NULL,
    purl text NOT NULL,
    package_name text NOT NULL,
    package_version text NOT NULL,
    severity text NOT NULL,
    summary text NOT NULL,
    cves text NOT NULL,
    fixed_versions text NOT NULL,
    cvss text NOT NULL,
    urls text NOT NULL,
    PRIMARY KEY (sha256, vuln_id, purl)
);

CREATE INDEX scan_findings_vuln ON scan_findings (vuln_id);

CREATE TABLE scan_issues (
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

CREATE TABLE scan_graphs (
    id text NOT NULL PRIMARY KEY,
    username text NOT NULL,
    status text NOT NULL CHECK (status IN ('queued', 'done', 'failed')),
    request text NOT NULL,
    result text,
    error text NOT NULL DEFAULT '',
    created timestamp NOT NULL,
    updated timestamp NOT NULL,
    build_name text,
    build_number text,
    project text
);

CREATE INDEX scan_graphs_build ON scan_graphs (build_name, build_number, project) WHERE build_name IS NOT NULL;
CREATE INDEX scan_graphs_created ON scan_graphs (created);

CREATE TABLE scan_policies (
    name text NOT NULL PRIMARY KEY,
    kind text NOT NULL,
    doc text NOT NULL,
    created timestamp NOT NULL,
    updated timestamp NOT NULL
);

CREATE TABLE scan_watches (
    name text NOT NULL PRIMARY KEY,
    doc text NOT NULL,
    created timestamp NOT NULL,
    updated timestamp NOT NULL
);

CREATE TABLE scan_ignore_rules (
    id text NOT NULL PRIMARY KEY,
    doc text NOT NULL,
    expires_at timestamp,
    created timestamp NOT NULL,
    created_by text NOT NULL
);

CREATE TABLE cves (
    id text NOT NULL PRIMARY KEY,
    title text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT '',
    published timestamp,
    updated timestamp,
    cwes text NOT NULL DEFAULT '[]',
    refs text NOT NULL DEFAULT '[]',
    cvss_score real,
    cvss_vector text NOT NULL DEFAULT '',
    cvss_version text NOT NULL DEFAULT '',
    cvss_source text NOT NULL DEFAULT '',
    ssvc_exploitation text NOT NULL DEFAULT '',
    ssvc_automatable text NOT NULL DEFAULT '',
    ssvc_impact text NOT NULL DEFAULT '',
    epss real,
    epss_percentile real,
    epss_date date,
    kev_added date,
    kev_due date,
    kev_ransomware boolean NOT NULL DEFAULT false,
    kev_action text NOT NULL DEFAULT '',
    record_checked timestamp,
    created timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f000+00:00', 'now'))
);

CREATE INDEX cves_record_due ON cves (record_checked);

CREATE TABLE cve_feeds (
    name text NOT NULL PRIMARY KEY,
    version text NOT NULL DEFAULT '',
    fetched timestamp NOT NULL
);

CREATE TABLE malicious_packages (
    id text NOT NULL,
    ecosystem text NOT NULL,
    name text NOT NULL,
    all_versions boolean NOT NULL,
    versions text NOT NULL,
    ranges text NOT NULL,
    summary text NOT NULL,
    modified timestamp NOT NULL,
    PRIMARY KEY (ecosystem, name, id)
);

CREATE TABLE malicious_feeds (
    ecosystem text NOT NULL PRIMARY KEY,
    etag text NOT NULL,
    fetched timestamp NOT NULL,
    changed timestamp NOT NULL
);

-- +goose Down
SELECT 'the baseline is not reverted';
