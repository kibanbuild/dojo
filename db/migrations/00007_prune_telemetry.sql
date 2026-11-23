-- +goose Up
-- Retention for telemetry_events (design 3.3): rows older than 12 months go. The nightly job runs
-- this as the migration role (owner), so it is SECURITY INVOKER and no service role may execute it:
-- dojo_api and dojo_admin stay without DELETE on the table. The 12 months are fixed, not a parameter,
-- so a wrong call cannot shorten them. One DELETE, not batches: a function is one transaction, so
-- batching inside it would not release any lock sooner, and the API only inserts new rows, which
-- never conflict with the old rows being deleted. The `at` index serves the range.
-- +goose StatementBegin
CREATE FUNCTION prune_telemetry() RETURNS bigint
    LANGUAGE sql
    SET search_path = pg_catalog, public
AS $$
    WITH deleted AS (
        DELETE FROM telemetry_events WHERE at < now() - interval '12 months' RETURNING 1
    )
    SELECT count(*) FROM deleted
$$;
-- +goose StatementEnd

REVOKE EXECUTE ON FUNCTION prune_telemetry() FROM PUBLIC;

-- +goose Down
DROP FUNCTION prune_telemetry();
