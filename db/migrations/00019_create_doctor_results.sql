-- +goose Up
-- doctor_results (design 3.3, 3.9): the latest result per learner and `dojo doctor` check, written by
-- the events projection (source cli) and, from PR 4, by the two self-confirmed steps (source learner).
-- text and fix are stored as the CLI sent them: untrusted, always rendered escaped. The events row
-- keeps neither. Cascades on learner_id, so delete_learner() (00011) erases it.
CREATE TABLE doctor_results (
    org_id     uuid NOT NULL REFERENCES orgs (id),
    learner_id uuid NOT NULL REFERENCES learners (id) ON DELETE CASCADE,
    check_id   text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    status     text NOT NULL CHECK (status IN ('ok', 'warn', 'fail', 'unknown')),
    text       text NOT NULL CHECK (char_length(text) BETWEEN 1 AND 200),
    fix        text NOT NULL DEFAULT '' CHECK (char_length(fix) <= 600),
    source     text NOT NULL CHECK (source IN ('cli', 'learner')),
    checked_at timestamptz NOT NULL,
    PRIMARY KEY (learner_id, check_id)
);

-- events.type gains doctor.run (the original constraint is the inline one of 00013).
ALTER TABLE events DROP CONSTRAINT events_type_check;
ALTER TABLE events ADD CONSTRAINT events_type_check CHECK (type IN
    ('ticket.started', 'lab.started', 'lab.failed', 'lab.stopped', 'check.run', 'hint.opened', 'doctor.run'));

-- Grants. dojo_api: the projection upserts (INSERT and UPDATE of the projected columns; org_id,
-- learner_id, check_id and created_at never change) and reads; DELETE is for the unconfirm route (PR 4).
-- dojo_admin only reads.
GRANT SELECT, DELETE ON doctor_results TO dojo_api;
GRANT INSERT (org_id, learner_id, check_id, status, text, fix, source, checked_at) ON doctor_results TO dojo_api;
GRANT UPDATE (updated_at, status, text, fix, source, checked_at) ON doctor_results TO dojo_api;
GRANT SELECT ON doctor_results TO dojo_admin;

-- +goose Down
DROP TABLE doctor_results;
DELETE FROM events WHERE type = 'doctor.run';
ALTER TABLE events DROP CONSTRAINT events_type_check;
ALTER TABLE events ADD CONSTRAINT events_type_check CHECK (type IN
    ('ticket.started', 'lab.started', 'lab.failed', 'lab.stopped', 'check.run', 'hint.opened'));
