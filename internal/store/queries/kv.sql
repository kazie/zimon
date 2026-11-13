-- name: GetKV :many
SELECT value FROM kv WHERE key = @key;

-- name: SetKV :exec
INSERT INTO kv(key, value) VALUES (@key, @value) ON CONFLICT(key) DO UPDATE SET value = excluded.value;
