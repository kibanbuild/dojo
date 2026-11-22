-- +goose Up
-- Rows that predate the column get a marker, then the default goes: every new learner states its version.
ALTER TABLE learners ADD COLUMN terms_version text NOT NULL DEFAULT 'unrecorded';
ALTER TABLE learners ALTER COLUMN terms_version DROP DEFAULT;
-- Lowercase letters, digits, dots and hyphens, 1 to 32: fits a date or a semver tag.
ALTER TABLE learners ADD CONSTRAINT learners_terms_version_format CHECK (terms_version ~ '^[0-9a-z.-]{1,32}$');
-- Set at creation with terms_accepted_at (design 3.1); no UPDATE for either role.
GRANT INSERT (terms_version) ON learners TO dojo_api;

-- +goose Down
ALTER TABLE learners DROP COLUMN terms_version;
