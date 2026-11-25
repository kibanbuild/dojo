-- +goose Up
-- email_log and mark_email_sent(id) (design 3.3 email_outbox row, 3.7), landing with the first email (E1, M3).
--
-- email_log: one row per mail sent to a learner, kind and sent_at (design 3.3), with the common
-- columns. E8 is never logged (the learner is gone). Rows go with the learner by cascade. No service
-- role has a grant: mark_email_sent() writes it as owner, and readers (E4 and E7 throttles) get their
-- own grant when they land.
CREATE TABLE email_log (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id     uuid NOT NULL REFERENCES orgs (id),
    learner_id uuid NOT NULL REFERENCES learners (id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('E1', 'E2', 'E3', 'E4', 'E5', 'E6', 'E7')),
    sent_at    timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX email_log_learner_id_idx ON email_log (learner_id, kind, sent_at);

-- mark_email_sent(id): the only way a service role changes an outbox row. SECURITY DEFINER, owned by
-- the migration role, with the search_path pinned (pg_temp last) like delete_learner(). It locks the
-- unsent row, then: for E8 deletes the row, so no address of a deleted learner stays; for any other
-- kind writes email_log (when the learner still exists), sets sent_at and clears to_address.
-- Returns true when it marked a row, false for an unknown or already sent id, so a second call after
-- a lost response is a no-op. EXECUTE goes to dojo_api only: the operator service never sends.
-- +goose StatementBegin
CREATE FUNCTION mark_email_sent(p_id uuid) RETURNS boolean
    LANGUAGE plpgsql
    SECURITY DEFINER
    SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    v_kind    text;
    v_learner uuid;
BEGIN
    SELECT kind, learner_id INTO v_kind, v_learner
    FROM email_outbox WHERE id = p_id AND sent_at IS NULL FOR UPDATE;
    IF NOT FOUND THEN
        RETURN false;
    END IF;

    IF v_kind = 'E8' THEN
        DELETE FROM email_outbox WHERE id = p_id;
        RETURN true;
    END IF;

    IF v_learner IS NOT NULL THEN
        INSERT INTO email_log (org_id, learner_id, kind)
        SELECT org_id, id, v_kind FROM learners WHERE id = v_learner;
    END IF;
    UPDATE email_outbox SET sent_at = now(), to_address = NULL WHERE id = p_id;
    RETURN true;
END
$$;
-- +goose StatementEnd

REVOKE EXECUTE ON FUNCTION mark_email_sent(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION mark_email_sent(uuid) TO dojo_api;

-- +goose Down
DROP FUNCTION mark_email_sent(uuid);
DROP TABLE email_log;
