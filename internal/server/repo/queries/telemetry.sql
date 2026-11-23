-- One batch of CLI events, all with the same learner (or none), CLI version and OS. A null org
-- (an anonymous batch) falls back to the public org. `at` is the database clock.
-- name: InsertTelemetryEvents :exec
INSERT INTO telemetry_events (org_id, learner_id, command, result, duration_ms, cli_version, os)
SELECT COALESCE(sqlc.narg(org_id)::uuid, (SELECT id FROM orgs WHERE slug = 'public')),
       sqlc.narg(learner_id)::uuid, e.command, e.result, e.duration_ms, @cli_version::text, @os::text
FROM jsonb_to_recordset(@events::jsonb) AS e(command text, result text, duration_ms integer);
