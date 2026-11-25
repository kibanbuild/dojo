-- +goose Up
-- One-line role summary (design 3.8): the Path page shows it under each role title. Set by the
-- catalog load from catalog.yaml, which requires it; the default only lets existing rows migrate.
--
-- Grants for both roles, as the design requires of every migration:
--   dojo_api:   unchanged, it already reads the whole catalog (00012).
--   dojo_admin: unchanged, table-level SELECT on roles (00014) covers the new column.
ALTER TABLE roles
    ADD COLUMN summary text NOT NULL DEFAULT '' CONSTRAINT roles_summary_len CHECK (char_length(summary) <= 120);

-- +goose Down
ALTER TABLE roles DROP COLUMN summary;
