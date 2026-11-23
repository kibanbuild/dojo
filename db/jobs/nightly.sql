-- The nightly job (design 3.7): runs as the migration role, which owns the tables.
-- Add prune_admin_audit() here when its table lands.
\set ON_ERROR_STOP on
SELECT prune_telemetry() AS telemetry_events_deleted \gset
\echo prune: telemetry_events deleted :telemetry_events_deleted
