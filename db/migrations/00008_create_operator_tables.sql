-- +goose Up
-- Operator service tables (design 3.3, 3.7). Platform level, so no org_id (like system_flags): an
-- operator is not a learner of any org. Only dojo_admin touches them; dojo_api has no grant.
CREATE TABLE operators (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    github_id    bigint NOT NULL UNIQUE,
    github_login text NOT NULL CHECK (char_length(github_login) BETWEEN 1 AND 39),
    added_by     uuid REFERENCES operators (id),
    added_at     timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz
);

-- A session is one hour from sign-in with no sliding: the CHECK pins expires_at to created_at, so
-- no update or odd insert can stretch it. Only the hash of the 256-bit id is stored.
CREATE TABLE admin_sessions (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    operator_id uuid NOT NULL REFERENCES operators (id),
    token_hash  bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL DEFAULT now() + interval '1 hour',
    revoked_at  timestamptz,
    CONSTRAINT admin_sessions_one_hour CHECK (expires_at = created_at + interval '1 hour')
);

CREATE INDEX admin_sessions_operator_id_idx ON admin_sessions (operator_id);

-- Append-only audit. operator_login is copied so the row survives a rename. before and after hold
-- changed fields only and never an email, username or login (design 3.3). No FK to the target: it
-- may be deleted (delete_learner) and the row must stay.
CREATE TABLE admin_audit (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    operator_id    uuid NOT NULL REFERENCES operators (id),
    operator_login text NOT NULL CHECK (char_length(operator_login) BETWEEN 1 AND 39),
    action         text NOT NULL CHECK (char_length(action) BETWEEN 1 AND 64),
    target_type    text NOT NULL CHECK (char_length(target_type) BETWEEN 1 AND 64),
    target_id      text NOT NULL CHECK (char_length(target_id) BETWEEN 1 AND 128),
    reason         text CHECK (char_length(reason) <= 1000),
    before         jsonb,
    after          jsonb,
    at             timestamptz NOT NULL DEFAULT now()
);

-- prune_admin_audit() deletes by age.
CREATE INDEX admin_audit_at_idx ON admin_audit (at);
CREATE INDEX admin_audit_operator_id_idx ON admin_audit (operator_id);

-- Revocation is one-way: once revoked_at is set it never changes, so a compromised dojo_admin cannot
-- un-revoke an operator or a session, nor rewrite the revoke time. The trigger also stops the owner.
-- +goose StatementBegin
CREATE FUNCTION refuse_revoked_at_change() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, public
AS $$
BEGIN
    IF OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at THEN
        RAISE EXCEPTION 'revoked_at is already set on % and cannot change', TG_TABLE_NAME
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

REVOKE EXECUTE ON FUNCTION refuse_revoked_at_change() FROM PUBLIC;

CREATE TRIGGER operators_revoked_at_one_way BEFORE UPDATE ON operators
    FOR EACH ROW EXECUTE FUNCTION refuse_revoked_at_change();
CREATE TRIGGER admin_sessions_revoked_at_one_way BEFORE UPDATE ON admin_sessions
    FOR EACH ROW EXECUTE FUNCTION refuse_revoked_at_change();

-- Grants (design 3.7). dojo_api has none on any of the three. dojo_admin, column-level:
--   operators: select; insert the identity and who added it; update revoked_at only (the first
--     row is written by `dojo-admin bootstrap` with the migration role). No DELETE.
--   admin_sessions: select; insert the operator and the hash (the id and the times keep their
--     defaults); update revoked_at only. No DELETE.
--   admin_audit: select and insert only. No role a service uses may update or delete it; rows go
--     through prune_admin_audit(), run by the nightly job with the migration role.
GRANT SELECT ON operators TO dojo_admin;
GRANT INSERT (github_id, github_login, added_by) ON operators TO dojo_admin;
GRANT UPDATE (revoked_at) ON operators TO dojo_admin;

GRANT SELECT ON admin_sessions TO dojo_admin;
GRANT INSERT (operator_id, token_hash) ON admin_sessions TO dojo_admin;
GRANT UPDATE (revoked_at) ON admin_sessions TO dojo_admin;

GRANT SELECT ON admin_audit TO dojo_admin;
GRANT INSERT (operator_id, operator_login, action, target_type, target_id, reason, before, after)
    ON admin_audit TO dojo_admin;

-- +goose Down
DROP TABLE admin_audit;
DROP TABLE admin_sessions;
DROP TABLE operators;
DROP FUNCTION refuse_revoked_at_change();
