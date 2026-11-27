-- +goose Up
-- A2 operator history (design 3.7) reads the newest admin_audit rows of one target on every learner
-- view; this index serves that read without scanning the table.
--
-- Grants for both roles, as the design requires of every migration: no change. dojo_api has no
-- grant on admin_audit and dojo_admin keeps select and insert only (00008); suspend and unsuspend
-- need nothing new, they use the existing UPDATE (updated_at, suspended_at) on learners (00003).
CREATE INDEX admin_audit_target_idx ON admin_audit (target_type, target_id, at DESC, id DESC);

-- +goose Down
DROP INDEX admin_audit_target_idx;
