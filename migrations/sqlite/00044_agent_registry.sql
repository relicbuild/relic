-- The agent registries' entries (MCP server definitions and skill bundles)
-- and their approvals: an entry is served only while its content is approved.

-- +goose Up
CREATE TABLE registry_entries (
    repo_key    text NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    path        text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('mcp', 'skills')),
    name        text NOT NULL,
    version     text NOT NULL,
    description text NOT NULL,
    sha256      text NOT NULL,
    doc         text NOT NULL,
    updated     timestamp NOT NULL,
    PRIMARY KEY (repo_key, path)
);
CREATE INDEX registry_entries_name ON registry_entries (repo_key, name);

CREATE TABLE approvals (
    repo_key    text NOT NULL REFERENCES repositories (key) ON DELETE CASCADE,
    path        text NOT NULL,
    sha256      text NOT NULL,
    approved_by text NOT NULL,
    approved    timestamp NOT NULL,
    PRIMARY KEY (repo_key, path)
);

-- +goose Down
DROP TABLE approvals;
DROP TABLE registry_entries;
