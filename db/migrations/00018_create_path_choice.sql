-- +goose Up
-- The path choice and the "Tell me when" waitlist (design 2.3, 3.9). The choice is the learner's own
-- column; a path that goes away leaves the learner with no choice, a learner that goes takes its waitlist rows.
--
-- Grants:
--   dojo_api:   learners gets INSERT (chosen_path_id) next to the sign-in list and UPDATE (chosen_path_id);
--               path_waitlist gets SELECT, INSERT (org_id, learner_id, path_id) and DELETE (a learner
--               leaves the list); created_at and updated_at keep their defaults and never change.
--   dojo_admin: SELECT on path_waitlist (the counts are the signal for the next path); the table-level
--               SELECT on learners (00003) already covers the new column.
ALTER TABLE learners
    ADD COLUMN chosen_path_id uuid REFERENCES paths (id) ON DELETE SET NULL;

CREATE TABLE path_waitlist (
    org_id     uuid NOT NULL REFERENCES orgs (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    learner_id uuid NOT NULL REFERENCES learners (id) ON DELETE CASCADE,
    path_id    uuid NOT NULL REFERENCES paths (id) ON DELETE CASCADE,
    PRIMARY KEY (learner_id, path_id)
);

CREATE INDEX path_waitlist_path_id_idx ON path_waitlist (path_id);

GRANT INSERT (chosen_path_id) ON learners TO dojo_api;
GRANT UPDATE (chosen_path_id) ON learners TO dojo_api;
GRANT SELECT, DELETE ON path_waitlist TO dojo_api;
GRANT INSERT (org_id, learner_id, path_id) ON path_waitlist TO dojo_api;
GRANT SELECT ON path_waitlist TO dojo_admin;

-- +goose Down
DROP TABLE path_waitlist;
ALTER TABLE learners DROP COLUMN chosen_path_id;
