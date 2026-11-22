-- +goose Up
-- A CLI token is a session for a device (design 3.1): same shape as sessions plus what S2 lists.
-- No expires_at: a CLI token lives until it is revoked. ssh_public_key (3.3) arrives with the exam
-- milestone that reads it. The length checks mirror what POST /v1/auth/cli accepts.
CREATE TABLE cli_tokens (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id       uuid NOT NULL REFERENCES orgs (id),
    learner_id   uuid NOT NULL REFERENCES learners (id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz,
    device_name  text NOT NULL CHECK (char_length(device_name) BETWEEN 1 AND 64),
    os           text NOT NULL CHECK (char_length(os) BETWEEN 1 AND 32),
    cli_version  text NOT NULL CHECK (char_length(cli_version) BETWEEN 1 AND 32)
);

CREATE INDEX cli_tokens_learner_id_idx ON cli_tokens (learner_id);

-- Grants (design 3.3, 3.4, 3.7), column-level like sessions. dojo_api inserts the row at login and
-- only touches last_used_at, revoked_at and updated_at afterwards; the token hash and the device
-- fields never change. No DELETE: a revoked row keeps its trail until the learner goes
-- (delete_learner() cascades as owner). dojo_admin has no grant: the operator service does not
-- manage CLI tokens (3.7), and the API refuses a suspended learner's tokens.
GRANT SELECT ON cli_tokens TO dojo_api;
GRANT INSERT (org_id, learner_id, token_hash, device_name, os, cli_version) ON cli_tokens TO dojo_api;
GRANT UPDATE (updated_at, last_used_at, revoked_at) ON cli_tokens TO dojo_api;

-- +goose Down
DROP TABLE cli_tokens;
