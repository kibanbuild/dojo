-- +goose Up
-- delete_learner(): the S1 transaction (design 3.5), one function so it is one transaction.
-- SECURITY DEFINER, owned by the migration role: dojo_api (the signed-in learner's own delete) and
-- dojo_admin (A2) have no DELETE on learners and no way to clear the other tables, by design, so
-- the function runs as owner. The search_path is pinned with pg_temp last, so no caller can shadow
-- a table or function with a temporary one.
-- EXECUTE goes only to those two roles (3.7); PUBLIC is revoked.
--
-- What it does today: locks the learner row (a concurrent second call waits, then finds nothing),
-- queues E8 with the address copied (ref = learner id) when the learner has an address, drops the
-- learner's unsent mail so no address stays in the outbox, and deletes the learner. Sessions, CLI
-- tokens and telemetry rows go by their ON DELETE CASCADE foreign keys, which also revokes every
-- token; email_outbox.learner_id is set null for the rows that remain.
--
-- Returns true when a learner was deleted, false for an unknown id: calling it again after a delete
-- (a double click, a retry after a lost response) is a no-op, never an error, and queues no second
-- E8. Later milestones extend this function: the retry-wait tombstone and the teardown_jobs row
-- for an open exam (M6a), and an explicit delete for any learner-owned table without a cascading
-- foreign key (attempts and the rest of section 3.3 as they land).
-- +goose StatementBegin
CREATE FUNCTION delete_learner(p_learner_id uuid) RETURNS boolean
    LANGUAGE plpgsql
    SECURITY DEFINER
    SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    v_email text;
BEGIN
    SELECT email INTO v_email FROM learners WHERE id = p_learner_id FOR UPDATE;
    IF NOT FOUND THEN
        RETURN false;
    END IF;

    IF v_email IS NOT NULL THEN
        INSERT INTO email_outbox (learner_id, to_address, kind, ref)
        VALUES (NULL, v_email, 'E8', p_learner_id::text)
        ON CONFLICT (kind, ref) DO NOTHING;
    END IF;

    DELETE FROM email_outbox WHERE learner_id = p_learner_id AND sent_at IS NULL AND kind <> 'E8';
    DELETE FROM learners WHERE id = p_learner_id;
    RETURN true;
END
$$;
-- +goose StatementEnd

REVOKE EXECUTE ON FUNCTION delete_learner(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION delete_learner(uuid) TO dojo_api, dojo_admin;

-- +goose Down
DROP FUNCTION delete_learner(uuid);
