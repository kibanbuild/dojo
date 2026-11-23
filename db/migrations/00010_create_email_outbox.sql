-- +goose Up
-- Email outbox (design 3.3): every email is queued here and the API's sender delivers it; the
-- operator service never sends. UNIQUE (kind, ref) makes each mail go once (ref per kind is in the
-- design: E1 the learner, E8 the learner id, and so on). to_address is copied when queued so E8 can
-- go after the learner row is gone; it is nullable because the sender clears it on send
-- (mark_email_sent(), added with the first email in M3). learner_id is nulled on delete.
-- No org_id: the design row has none, and the outbox is a platform queue, not a learner-owned table.
CREATE TABLE email_outbox (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    learner_id uuid REFERENCES learners (id) ON DELETE SET NULL,
    to_address text CHECK (char_length(to_address) BETWEEN 3 AND 320),
    kind       text NOT NULL CHECK (kind IN ('E1', 'E2', 'E3', 'E4', 'E5', 'E6', 'E7', 'E8')),
    ref        text NOT NULL CHECK (char_length(ref) BETWEEN 1 AND 256),
    sent_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT email_outbox_kind_ref_key UNIQUE (kind, ref)
);

CREATE INDEX email_outbox_learner_id_idx ON email_outbox (learner_id) WHERE learner_id IS NOT NULL;
-- The sender scans unsent rows.
CREATE INDEX email_outbox_unsent_idx ON email_outbox (created_at) WHERE sent_at IS NULL;

-- Grants (design 3.7), column-level so id, sent_at and created_at keep their defaults. dojo_api and
-- dojo_admin may insert (their own E1 to E8) and select, nothing else: no UPDATE and no DELETE. The
-- sender marks a row sent only through mark_email_sent() (M3), and E8 and unsent rows of a deleted
-- learner go through delete_learner() as owner.
GRANT SELECT ON email_outbox TO dojo_api, dojo_admin;
GRANT INSERT (learner_id, to_address, kind, ref) ON email_outbox TO dojo_api, dojo_admin;

-- +goose Down
DROP TABLE email_outbox;
