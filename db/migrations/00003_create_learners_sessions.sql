-- +goose Up
CREATE TABLE learners (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id            uuid NOT NULL REFERENCES orgs (id),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    github_id         bigint NOT NULL UNIQUE,
    github_login      text NOT NULL,
    username          text NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9-]{3,30}$'),
    email             text,
    language          text NOT NULL DEFAULT 'en' CHECK (language IN ('en', 'es')),
    age_confirmed_at  timestamptz NOT NULL,
    terms_accepted_at timestamptz NOT NULL,
    usage_stats       boolean NOT NULL DEFAULT true,
    analytics_consent boolean,
    suspended_at      timestamptz
);

-- No expires_at: design 3.1 says a 30-day sliding expiry, so it is last_used_at plus 30 days.
CREATE TABLE sessions (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id       uuid NOT NULL REFERENCES orgs (id),
    learner_id   uuid NOT NULL REFERENCES learners (id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz
);

CREATE INDEX sessions_learner_id_idx ON sessions (learner_id);

-- Grants (design 3.3, 3.4, 3.7). Column-level, so an ungranted column keeps its default on insert.
-- dojo_api: sign-in creates a learner (age and terms are set only here), settings and consent
-- update the learner's own columns. It may not touch github_id, org_id or suspended_at, and has
-- no DELETE: an account goes only through delete_learner() (3.5), whose cascade runs as owner.
GRANT SELECT ON learners TO dojo_api;
GRANT INSERT (org_id, github_id, github_login, username, email, language,
              age_confirmed_at, terms_accepted_at, usage_stats)
    ON learners TO dojo_api;
GRANT UPDATE (updated_at, github_login, username, email, language, usage_stats, analytics_consent)
    ON learners TO dojo_api;
-- Sessions are revoked by setting revoked_at (3.1 keeps hashes and revocation, 3.5 revokes on
-- delete), so there is no DELETE and a session row keeps its trail until the learner goes.
GRANT SELECT ON sessions TO dojo_api;
GRANT INSERT (org_id, learner_id, token_hash) ON sessions TO dojo_api;
GRANT UPDATE (updated_at, last_used_at, revoked_at) ON sessions TO dojo_api;
-- dojo_admin (3.7): A2 reads learners and "update learners.suspended_at", nothing else; the API
-- refuses a suspended learner's sessions, so the operator service needs no grant on sessions.
GRANT SELECT ON learners TO dojo_admin;
GRANT UPDATE (updated_at, suspended_at) ON learners TO dojo_admin;

-- +goose Down
DROP TABLE sessions;
DROP TABLE learners;
