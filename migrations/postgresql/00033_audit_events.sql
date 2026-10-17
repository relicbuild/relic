-- The audit log: changes to administration settings, sign-ins, tokens and blocked downloads.

-- +goose Up
CREATE TABLE audit_events (
    id      uuid        PRIMARY KEY,
    time    timestamptz NOT NULL,
    actor   text        NOT NULL,
    action  text        NOT NULL,
    target  text        NOT NULL DEFAULT '',
    outcome text        NOT NULL CHECK (outcome IN ('success', 'denied', 'failure')),
    client  text        NOT NULL DEFAULT '',
    detail  text        NOT NULL DEFAULT ''
);
CREATE INDEX audit_events_time ON audit_events (time);
CREATE INDEX audit_events_actor ON audit_events (actor, id);

-- +goose Down
DROP TABLE audit_events;
