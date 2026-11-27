-- +goose Up
-- Where the learner's email came from, and whether they have left the first-run Today (design 3.9).
-- email_source is null with no email; rows that already hold an email came from GitHub, the only source until now.
--
-- Grants:
--   dojo_api:   INSERT (email_source) next to the sign-in list (00018), UPDATE (email_source, first_run_seen_at)
--               next to updated_at; email is already updatable (00003).
--   dojo_admin: the table-level SELECT on learners (00003) already covers the new columns.
ALTER TABLE learners
    ADD COLUMN email_source text CHECK (email_source IN ('github', 'learner')),
    ADD COLUMN first_run_seen_at timestamptz;

UPDATE learners SET email_source = 'github' WHERE email IS NOT NULL;

GRANT INSERT (email_source) ON learners TO dojo_api;
GRANT UPDATE (email_source, first_run_seen_at) ON learners TO dojo_api;

-- +goose Down
ALTER TABLE learners
    DROP COLUMN first_run_seen_at,
    DROP COLUMN email_source;
