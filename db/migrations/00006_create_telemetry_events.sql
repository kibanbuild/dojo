-- +goose Up
-- CLI usage stats (design 1.7, 3.3): one row per command the CLI ran. learner_id is null for an
-- event sent without a token (before dojo login). No IP address, no arguments, no output: only the
-- command name, its result and timing, and the CLI build. `at` is the server clock at receipt.
-- The bounds mirror what POST /v1/telemetry accepts.
CREATE TABLE telemetry_events (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id      uuid NOT NULL REFERENCES orgs (id),
    learner_id  uuid REFERENCES learners (id) ON DELETE CASCADE,
    at          timestamptz NOT NULL DEFAULT now(),
    command     text NOT NULL CHECK (command ~ '^[a-z][a-z0-9_-]{0,31}$'),
    result      text NOT NULL CHECK (result ~ '^[a-z][a-z0-9_-]{0,31}$'),
    duration_ms integer NOT NULL CHECK (duration_ms BETWEEN 0 AND 86400000),
    cli_version text NOT NULL CHECK (char_length(cli_version) BETWEEN 1 AND 32),
    os          text NOT NULL CHECK (char_length(os) BETWEEN 1 AND 32)
);

-- prune_telemetry() deletes by age; the partial index serves the learner cascade.
CREATE INDEX telemetry_events_at_idx ON telemetry_events (at);
CREATE INDEX telemetry_events_learner_id_idx ON telemetry_events (learner_id) WHERE learner_id IS NOT NULL;

-- Grants (design 3.3, 3.7). dojo_api only inserts: no SELECT (the API never reads events back),
-- no UPDATE, no DELETE. Rows go with the learner (delete_learner() cascades as owner) or after
-- 12 months through prune_telemetry(), run by the nightly job with the migration role. `at` and
-- `id` keep their defaults. dojo_admin has no grant: no operator screen reads telemetry (3.7).
GRANT INSERT (org_id, learner_id, command, result, duration_ms, cli_version, os)
    ON telemetry_events TO dojo_api;

-- +goose Down
DROP TABLE telemetry_events;
