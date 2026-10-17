-- name: GetKeyPair :one
SELECT * FROM key_pairs WHERE name = @name;

-- name: InsertKeyPair :exec
-- A concurrent insert of the same name wins; callers read it back.
INSERT INTO key_pairs (name, kind, private_key, public_key)
VALUES (@name, @kind, @private_key, @public_key)
ON CONFLICT (name) DO NOTHING;

-- name: ListKeyPairs :many
SELECT * FROM key_pairs ORDER BY name;

-- name: UpdateKeyPairPrivateKey :exec
UPDATE key_pairs SET private_key = @private_key WHERE name = @name;
