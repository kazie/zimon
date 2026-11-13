-- States are store's Job constants, passed in.

-- name: AddJob :one
INSERT INTO jobs(kind, summary, by, state, created_at) VALUES (@kind, @summary, @by, @queued, @now) RETURNING *;

-- name: StartJob :exec
UPDATE jobs SET state = @running, started_at = @now WHERE id = @id;

-- name: AddJobLog :exec
UPDATE jobs SET log = CASE log WHEN '' THEN CAST(@line AS TEXT) ELSE log || char(10) || @line END WHERE id = @id;

-- name: FinishJob :exec
UPDATE jobs SET state = @state, error = @error, finished_at = @now WHERE id = @id;

-- name: PruneJobs :exec
-- All but the newest keep.
DELETE FROM jobs WHERE id <= (SELECT j.id FROM jobs j ORDER BY j.id DESC LIMIT 1 OFFSET @keep);

-- name: InterruptJobs :exec
UPDATE jobs SET state = @interrupted, finished_at = @now WHERE state IN (@queued, @running);

-- name: Jobs :many
SELECT * FROM jobs ORDER BY id DESC LIMIT @lim;

-- name: JobByID :many
SELECT * FROM jobs WHERE id = @id;
