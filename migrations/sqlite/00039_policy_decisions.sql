-- Policy decisions that blocked something: a download, a build or a
-- promotion. One row per kind, subject, policy, watch and rule, counting its
-- occurrences.

-- +goose Up
CREATE TABLE policy_decisions (
    kind       text    NOT NULL CHECK (kind IN ('download', 'build', 'release')),
    subject    text    NOT NULL,
    policy     text    NOT NULL,
    watch      text    NOT NULL,
    rule       text    NOT NULL,
    reason     text    NOT NULL,
    actor      text    NOT NULL,
    count      integer NOT NULL,
    first_seen timestamp NOT NULL,
    last_seen  timestamp NOT NULL,
    PRIMARY KEY (kind, subject, policy, watch, rule)
);
CREATE INDEX policy_decisions_last_seen ON policy_decisions (last_seen);

-- +goose Down
DROP TABLE policy_decisions;
