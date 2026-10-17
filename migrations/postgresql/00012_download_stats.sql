-- +goose Up
CREATE TABLE download_stats (
    item_id            uuid        PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    downloads          bigint      NOT NULL,
    last_downloaded    timestamptz NOT NULL,
    last_downloaded_by text        NOT NULL
);

-- +goose Down
DROP TABLE download_stats;
