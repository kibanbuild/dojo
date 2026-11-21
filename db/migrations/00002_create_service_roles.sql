-- +goose Up
-- Roles are cluster-wide and carry no password here; the db-roles job turns on login.
-- Every later table migration grants explicitly to both roles (technical design 3.3, 3.7).
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dojo_api') THEN
        CREATE ROLE dojo_api NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dojo_admin') THEN
        CREATE ROLE dojo_admin NOLOGIN;
    END IF;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO dojo_api, dojo_admin;
GRANT SELECT ON orgs TO dojo_api, dojo_admin;

-- +goose Down
DROP OWNED BY dojo_api, dojo_admin;
DROP ROLE dojo_api, dojo_admin;
