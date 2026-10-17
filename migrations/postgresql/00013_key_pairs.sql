-- +goose Up
CREATE TABLE key_pairs (
    name        text        PRIMARY KEY,
    kind        text        NOT NULL CHECK (kind IN ('rsa', 'gpg')),
    private_key bytea       NOT NULL,
    public_key  text        NOT NULL,
    created     timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE key_pairs;
