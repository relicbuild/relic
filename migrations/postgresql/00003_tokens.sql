-- +goose Up
CREATE TABLE signing_keys (
    kid         text        PRIMARY KEY,
    private_key bytea       NOT NULL,
    created     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE access_tokens (
    id                 uuid        PRIMARY KEY,
    username           text        NOT NULL,
    scope              text        NOT NULL,
    refresh_token_hash text UNIQUE,
    expires            timestamptz,
    created            timestamptz NOT NULL DEFAULT now(),
    revoked            boolean     NOT NULL DEFAULT false
);
CREATE INDEX access_tokens_username ON access_tokens (username);

-- +goose Down
DROP TABLE access_tokens;
DROP TABLE signing_keys;
