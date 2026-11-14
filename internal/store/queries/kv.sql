-- name: GetKV :many
SELECT value FROM kv WHERE key = @key;

-- name: SetKV :exec
INSERT INTO kv(key, value) VALUES (@key, @value) ON CONFLICT(key) DO UPDATE SET value = excluded.value;

-- name: DeleteKV :exec
DELETE FROM kv WHERE key = @key;

-- name: DeleteExpiredSessions :exec
DELETE FROM kv WHERE key GLOB @pattern AND json_valid(value) AND json_extract(value, '$.expires') < CAST(@now AS INTEGER);
