-- +goose Up
-- Retention for admin_audit (design 3.3): rows older than 2 years go (proposed, to confirm with the
-- legal review). Same shape as prune_telemetry(): SECURITY INVOKER, run by the nightly job with the
-- migration role (owner), EXECUTE revoked from everyone so no service role gains DELETE through it.
-- The 2 years are fixed, not a parameter, so a wrong call cannot shorten them.
-- +goose StatementBegin
CREATE FUNCTION prune_admin_audit() RETURNS bigint
    LANGUAGE sql
    SET search_path = pg_catalog, public
AS $$
    WITH deleted AS (
        DELETE FROM admin_audit WHERE at < now() - interval '2 years' RETURNING 1
    )
    SELECT count(*) FROM deleted
$$;
-- +goose StatementEnd

REVOKE EXECUTE ON FUNCTION prune_admin_audit() FROM PUBLIC;

-- +goose Down
DROP FUNCTION prune_admin_audit();
