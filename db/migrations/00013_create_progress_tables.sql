-- +goose Up
-- Progress tables (design 3.3, 3.8): events (append-only audit), ticket_progress (the projection the
-- API writes in the same transaction as the event) and hint_opens (written hints a learner opened).
-- item_id is the catalog item id (text, for example support-l1/perms-03), not a uuid. ticket_progress
-- and hint_opens reference catalog_items (id) with NO ACTION, never CASCADE: a catalog load that drops
-- an item someone has progress on fails instead of erasing that progress. events keeps item ids in
-- its payload with no FK, as an audit log. The design makes an unknown item an "ignored" event that
-- projects nothing, so the API checks the catalog before it writes either table.
-- Every learner FK is ON DELETE CASCADE, so delete_learner() (00011) erases all three tables.

CREATE TABLE events (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id          uuid NOT NULL REFERENCES orgs (id),
    learner_id      uuid NOT NULL REFERENCES learners (id) ON DELETE CASCADE,
    client_event_id uuid NOT NULL,
    type            text NOT NULL CHECK (type IN
                        ('ticket.started', 'lab.started', 'lab.failed', 'lab.stopped', 'check.run', 'hint.opened')),
    payload         jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload) = 'object'),
    at              timestamptz NOT NULL DEFAULT now(),
    client_at       timestamptz NOT NULL,
    CONSTRAINT events_learner_client_event_key UNIQUE (learner_id, client_event_id)
);

-- prune_events() deletes by age. The unique key above leads with learner_id and serves the cascade.
CREATE INDEX events_at_idx ON events (at);

CREATE TABLE ticket_progress (
    org_id            uuid NOT NULL REFERENCES orgs (id),
    learner_id        uuid NOT NULL REFERENCES learners (id) ON DELETE CASCADE,
    item_id           text NOT NULL REFERENCES catalog_items (id),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    state             text NOT NULL CHECK (state IN ('started', 'passed', 'revisit')),
    lab               text CHECK (lab IN ('local', 'hosted')),
    lab_state         text CHECK (lab_state IN ('starting', 'running', 'failed', 'stopped', 'expired')),
    lab_state_at      timestamptz,
    first_started_at  timestamptz NOT NULL,
    last_seen_at      timestamptz NOT NULL DEFAULT now(),
    check_count       integer NOT NULL DEFAULT 0 CHECK (check_count >= 0),
    last_check_at     timestamptz,
    last_check_ok     boolean,
    last_check_passed integer CHECK (last_check_passed BETWEEN 0 AND 1000),
    last_check_total  integer CHECK (last_check_total BETWEEN 0 AND 1000),
    -- At most 16,384 bytes, as POST /v1/events accepts (3.8); it counts bytes, not characters.
    last_check_output text CHECK (octet_length(last_check_output) <= 16384),
    first_passed_at   timestamptz,
    passed_seconds    integer CHECK (passed_seconds >= 0),
    passed_commit     text,
    PRIMARY KEY (learner_id, item_id),
    -- passed and total come together, and passed never exceeds total.
    CONSTRAINT ticket_progress_check_counts CHECK (
        (last_check_passed IS NULL) = (last_check_total IS NULL)
        AND (last_check_passed IS NULL OR last_check_passed <= last_check_total)),
    -- A passed or revisit ticket always carries the time of its first pass.
    CONSTRAINT ticket_progress_passed_has_time CHECK (state = 'started' OR first_passed_at IS NOT NULL)
);

-- The expiry sweeper (3.8) scans live labs by age.
CREATE INDEX ticket_progress_live_lab_idx ON ticket_progress (last_seen_at) WHERE lab_state IN ('starting', 'running');

CREATE TABLE hint_opens (
    org_id     uuid NOT NULL REFERENCES orgs (id),
    learner_id uuid NOT NULL REFERENCES learners (id) ON DELETE CASCADE,
    item_id    text NOT NULL REFERENCES catalog_items (id),
    n          smallint NOT NULL CHECK (n BETWEEN 1 AND 3),
    opened_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (learner_id, item_id, n)
);

-- Grants (design 3.3, 3.7). No role a service uses gets DELETE or TRUNCATE: rows go by the learner
-- cascade (delete_learner() as owner) or, for events, prune_events() as the migration role.
-- events: the API inserts and reads (ON CONFLICT DO NOTHING ... RETURNING tells a duplicate from a
-- new event); no UPDATE, the log is append-only. id and at keep their defaults. dojo_admin has none.
GRANT SELECT ON events TO dojo_api;
GRANT INSERT (org_id, learner_id, client_event_id, type, payload, client_at) ON events TO dojo_api;

-- ticket_progress: the projection inserts and updates every projected column. org_id, learner_id,
-- item_id, created_at and passed_commit (M3) never change from the API. dojo_admin reads it (A2).
GRANT SELECT ON ticket_progress TO dojo_api, dojo_admin;
GRANT INSERT (org_id, learner_id, item_id, state, lab, lab_state, lab_state_at, first_started_at, last_seen_at,
              check_count, last_check_at, last_check_ok, last_check_passed, last_check_total, last_check_output,
              first_passed_at, passed_seconds)
    ON ticket_progress TO dojo_api;
GRANT UPDATE (updated_at, state, lab, lab_state, lab_state_at, first_started_at, last_seen_at, check_count,
              last_check_at, last_check_ok, last_check_passed, last_check_total, last_check_output,
              first_passed_at, passed_seconds)
    ON ticket_progress TO dojo_api;

-- hint_opens: the API inserts and reads; a repeat is ON CONFLICT DO NOTHING. No UPDATE, no admin grant.
GRANT SELECT ON hint_opens TO dojo_api;
GRANT INSERT (org_id, learner_id, item_id, n) ON hint_opens TO dojo_api;

-- Retention for events (design 3.3): rows older than 12 months go. Same shape as prune_telemetry():
-- SECURITY INVOKER, run by the nightly job with the migration role (owner), EXECUTE revoked from
-- everyone so no service role gains DELETE through it. The 12 months are fixed, not a parameter, and
-- the `at` index serves the range. ticket_progress is the learner's record and is never pruned.
-- +goose StatementBegin
CREATE FUNCTION prune_events() RETURNS bigint
    LANGUAGE sql
    SET search_path = pg_catalog, public
AS $$
    WITH deleted AS (
        DELETE FROM events WHERE at < now() - interval '12 months' RETURNING 1
    )
    SELECT count(*) FROM deleted
$$;
-- +goose StatementEnd

REVOKE EXECUTE ON FUNCTION prune_events() FROM PUBLIC;

-- +goose Down
DROP FUNCTION prune_events();
DROP TABLE hint_opens;
DROP TABLE ticket_progress;
DROP TABLE events;
