-- The nightly job (design 3.7): runs as the migration role, which owns the tables.
\set ON_ERROR_STOP on
SELECT prune_telemetry() AS telemetry_events_deleted \gset
\echo prune: telemetry_events deleted :telemetry_events_deleted
SELECT prune_admin_audit() AS admin_audit_deleted \gset
\echo prune: admin_audit deleted :admin_audit_deleted
