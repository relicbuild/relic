-- Waiver requests: a user asks for an ignore rule; an administrator approves
-- it with an expiry, or rejects it.

-- +goose Up
CREATE TABLE waivers (
    id             text NOT NULL PRIMARY KEY,
    requested_by   text NOT NULL,
    reason         text NOT NULL,
    doc            text NOT NULL,
    status         text NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
    decided_by     text NOT NULL DEFAULT '',
    decision_note  text NOT NULL DEFAULT '',
    decided        timestamp,
    expires        timestamp,
    ignore_rule_id text NOT NULL DEFAULT '',
    created        timestamp NOT NULL
);
CREATE INDEX waivers_status ON waivers (status, created);

-- +goose Down
DROP TABLE waivers;
