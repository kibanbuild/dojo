-- +goose Up
-- ticket_reports (design 3.9, "The report form"): a learner's report of a broken ticket or a request for setup
-- help, forwarded by email to us by the API's reader, which sets emailed_at. item_id has no foreign key, so
-- retiring a ticket never blocks a catalog load. last_check_output and doctor_summary are copied by the API from
-- the learner's own rows, never taken from the client. Cascades on learner_id, so delete_learner() (00011)
-- erases it and no personal data outlives the account.
CREATE TABLE ticket_reports (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id            uuid NOT NULL REFERENCES orgs (id),
    learner_id        uuid NOT NULL REFERENCES learners (id) ON DELETE CASCADE,
    item_id           text,
    kind              text NOT NULL CHECK (kind IN ('broken_ticket', 'setup_help')),
    cli_version       text NOT NULL DEFAULT '',
    last_check_output text CHECK (octet_length(last_check_output) <= 16384),
    message           text NOT NULL CHECK (char_length(message) BETWEEN 1 AND 2000),
    doctor_summary    text NOT NULL DEFAULT '' CHECK (char_length(doctor_summary) <= 2000),
    emailed_at        timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ticket_reports_broken_has_item CHECK (kind <> 'broken_ticket' OR item_id IS NOT NULL)
);

-- The rate limit counts a learner's recent reports; the reader scans the unsent ones oldest first.
CREATE INDEX ticket_reports_learner_created_idx ON ticket_reports (learner_id, created_at);
CREATE INDEX ticket_reports_unsent_idx ON ticket_reports (created_at) WHERE emailed_at IS NULL;

-- Grants. dojo_api inserts the report and the reader sets emailed_at (UPDATE is also what SELECT ... FOR UPDATE
-- SKIP LOCKED needs); nothing else changes after the insert. dojo_admin only reads.
GRANT SELECT ON ticket_reports TO dojo_api;
GRANT INSERT (org_id, learner_id, item_id, kind, cli_version, last_check_output, message, doctor_summary)
    ON ticket_reports TO dojo_api;
GRANT UPDATE (updated_at, emailed_at) ON ticket_reports TO dojo_api;
GRANT SELECT ON ticket_reports TO dojo_admin;

-- +goose Down
DROP TABLE ticket_reports;
