-- +goose Up
-- Signed commit on a pass (design 3.9): the sha the CLI reports in a ticket.commit event, checked in the
-- learner's public repo by the API's verifier. commit_state is null until a sha is accepted; commit_next_at
-- is the verifier's lease and schedule, commit_at the time the sha was accepted (the 7-day clock).
ALTER TABLE ticket_progress
    ADD COLUMN commit_state      text CHECK (commit_state IN ('pending', 'verified', 'unverified')),
    ADD COLUMN commit_tries      integer NOT NULL DEFAULT 0 CHECK (commit_tries >= 0),
    ADD COLUMN commit_next_at    timestamptz,
    ADD COLUMN commit_checked_at timestamptz,
    ADD COLUMN commit_at         timestamptz,
    ADD CONSTRAINT ticket_progress_passed_commit_format CHECK (passed_commit ~ '^[0-9a-f]{40}$');

-- The verifier's claim query scans pending rows by due time.
CREATE INDEX ticket_progress_commit_due_idx ON ticket_progress (commit_next_at) WHERE commit_state = 'pending';

-- A sha that arrived before its pass: applied and deleted when the pass projects, first sha wins.
-- item_id references catalog_items with NO ACTION, like ticket_progress: a catalog load cannot drop an item that has one.
CREATE TABLE pending_commits (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id     uuid NOT NULL REFERENCES orgs (id),
    learner_id uuid NOT NULL REFERENCES learners (id) ON DELETE CASCADE,
    item_id    text NOT NULL REFERENCES catalog_items (id),
    "commit"   text NOT NULL CHECK ("commit" ~ '^[0-9a-f]{40}$'),
    client_at  timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pending_commits_learner_item_key UNIQUE (learner_id, item_id)
);

-- events.type gains ticket.commit (the constraint is the one 00019 left, with doctor.run).
ALTER TABLE events DROP CONSTRAINT events_type_check;
ALTER TABLE events ADD CONSTRAINT events_type_check CHECK (type IN
    ('ticket.started', 'lab.started', 'lab.failed', 'lab.stopped', 'check.run', 'hint.opened', 'doctor.run', 'ticket.commit'));

-- Retention for pending_commits: a sha whose pass never came goes after 30 days. Same shape as
-- prune_events(): SECURITY INVOKER, run by the nightly job as the owner, EXECUTE revoked from everyone.
-- +goose StatementBegin
CREATE FUNCTION prune_pending_commits() RETURNS bigint
    LANGUAGE sql
    SET search_path = pg_catalog, public
AS $$
    WITH deleted AS (
        DELETE FROM pending_commits WHERE created_at < now() - interval '30 days' RETURNING 1
    )
    SELECT count(*) FROM deleted
$$;
-- +goose StatementEnd

REVOKE EXECUTE ON FUNCTION prune_pending_commits() FROM PUBLIC;

-- Grants. ticket_progress: the projection (ticket.commit) and the verifier write the commit columns; the
-- INSERT grant is unchanged, a row never starts with a commit. pending_commits: the projection inserts
-- (a repeat is ON CONFLICT DO NOTHING, so no UPDATE) and deletes the row it applies; dojo_admin reads.
GRANT UPDATE (commit_state, commit_tries, commit_next_at, commit_checked_at, commit_at, passed_commit)
    ON ticket_progress TO dojo_api;
GRANT SELECT, DELETE ON pending_commits TO dojo_api;
GRANT INSERT (org_id, learner_id, item_id, "commit", client_at) ON pending_commits TO dojo_api;
GRANT SELECT ON pending_commits TO dojo_admin;

-- +goose Down
REVOKE UPDATE (passed_commit) ON ticket_progress FROM dojo_api;
DROP FUNCTION prune_pending_commits();
DROP TABLE pending_commits;
DELETE FROM events WHERE type = 'ticket.commit';
ALTER TABLE events DROP CONSTRAINT events_type_check;
ALTER TABLE events ADD CONSTRAINT events_type_check CHECK (type IN
    ('ticket.started', 'lab.started', 'lab.failed', 'lab.stopped', 'check.run', 'hint.opened', 'doctor.run'));
DROP INDEX ticket_progress_commit_due_idx;
ALTER TABLE ticket_progress
    DROP CONSTRAINT ticket_progress_passed_commit_format,
    DROP COLUMN commit_at,
    DROP COLUMN commit_checked_at,
    DROP COLUMN commit_next_at,
    DROP COLUMN commit_tries,
    DROP COLUMN commit_state;
