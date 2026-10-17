-- Webhooks: URLs subscribed to event types, and the deliveries of each event
-- to them, queued here and sent by the worker.

-- +goose Up
CREATE TABLE webhooks (
    name    text NOT NULL PRIMARY KEY,
    config  text NOT NULL,
    created timestamp NOT NULL,
    updated timestamp NOT NULL
);

CREATE TABLE webhook_deliveries (
    id              text NOT NULL PRIMARY KEY,
    webhook         text NOT NULL REFERENCES webhooks (name) ON DELETE CASCADE,
    event           text NOT NULL,
    payload         text NOT NULL,
    status          text NOT NULL CHECK (status IN ('pending', 'delivered', 'failed')),
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt    timestamp NOT NULL,
    response_status integer NOT NULL DEFAULT 0,
    error           text NOT NULL DEFAULT '',
    created         timestamp NOT NULL,
    delivered       timestamp
);
CREATE INDEX webhook_deliveries_due ON webhook_deliveries (status, next_attempt);
CREATE INDEX webhook_deliveries_webhook ON webhook_deliveries (webhook, created);

-- +goose Down
DROP TABLE webhook_deliveries;
DROP TABLE webhooks;
