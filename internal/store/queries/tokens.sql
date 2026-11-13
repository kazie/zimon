-- name: AddToken :exec
INSERT INTO tokens(name, sha256, created_at) VALUES (@name, @sha256, @now);

-- name: Tokens :many
SELECT * FROM tokens ORDER BY name;

-- name: RemoveToken :execrows
DELETE FROM tokens WHERE name = @name;

-- name: TokenUsed :exec
UPDATE tokens SET last_used_at = @last_used_at WHERE sha256 = @sha256;

-- name: TokenByHash :many
SELECT * FROM tokens WHERE sha256 = @sha256;
