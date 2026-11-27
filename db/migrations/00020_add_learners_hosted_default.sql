-- +goose Up
-- O3 blocked: hosted by default (design 3.9). A learner whose laptop cannot run labs sets it; next_step then
-- skips o3 and `dojo start` says hosted labs are coming soon.
--
-- Grants:
--   dojo_api:   UPDATE (hosted_default) next to updated_at (PATCH /v1/me); no INSERT, a new learner takes the default.
--   dojo_admin: the table-level SELECT on learners (00003) already covers the new column.
ALTER TABLE learners
    ADD COLUMN hosted_default boolean NOT NULL DEFAULT false;

GRANT UPDATE (updated_at, hosted_default) ON learners TO dojo_api;

-- +goose Down
ALTER TABLE learners DROP COLUMN hosted_default;
