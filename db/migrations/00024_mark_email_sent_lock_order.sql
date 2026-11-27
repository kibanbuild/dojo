-- +goose Up
-- mark_email_sent() took the outbox row first and then reached for the learner (the email_log foreign key
-- check locks the learner row FOR KEY SHARE). delete_learner() and a plain DELETE of a learner lock the
-- learner row first and then update its outbox rows, so the two could wait on each other (SQLSTATE 40P01).
-- Now it takes the same order as the delete: the learner row first (FOR KEY SHARE, which a delete's
-- FOR UPDATE conflicts with), then the outbox row. The learner is found without a lock by reading the
-- row first; if the learner is deleted in between, the locked re-read sees learner_id already null.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION mark_email_sent(p_id uuid) RETURNS boolean
    LANGUAGE plpgsql
    SECURITY DEFINER
    SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    v_kind    text;
    v_learner uuid;
BEGIN
    SELECT learner_id INTO v_learner FROM email_outbox WHERE id = p_id AND sent_at IS NULL;
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    IF v_learner IS NOT NULL THEN
        PERFORM 1 FROM learners WHERE id = v_learner FOR KEY SHARE;
    END IF;

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

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION mark_email_sent(p_id uuid) RETURNS boolean
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
