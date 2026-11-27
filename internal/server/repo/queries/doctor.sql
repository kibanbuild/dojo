-- doctor_results (design 3.9): the latest result per learner and check.

-- The doctor.run projection: an older run (client_at before checked_at) changes nothing and returns
-- no row. updated_at moves on every UPDATE.
-- name: UpsertDoctorResult :one
INSERT INTO doctor_results (org_id, learner_id, check_id, status, text, fix, source, checked_at)
VALUES (@org_id, @learner_id, @check_id, @status, @text, @fix, 'cli', @checked_at)
ON CONFLICT (learner_id, check_id) DO UPDATE
SET updated_at = now(), status = excluded.status, text = excluded.text, fix = excluded.fix,
    source = excluded.source, checked_at = excluded.checked_at
WHERE doctor_results.checked_at <= excluded.checked_at
RETURNING check_id, status;

-- name: ListDoctorResults :many
SELECT check_id, status, text, fix, source, checked_at
FROM doctor_results WHERE learner_id = @learner_id ORDER BY check_id;

-- The learner confirms an advisory step (design 3.9); the org comes from the learner row.
-- name: ConfirmDoctorStep :exec
INSERT INTO doctor_results (org_id, learner_id, check_id, status, text, fix, source, checked_at)
SELECT l.org_id, l.id, @check_id, 'ok', 'Confirmed by you', '', 'learner', now()
FROM learners l WHERE l.id = @learner_id
ON CONFLICT (learner_id, check_id) DO UPDATE
SET updated_at = now(), status = 'ok', text = excluded.text, fix = '', source = 'learner', checked_at = now();

-- name: UnconfirmDoctorStep :execrows
DELETE FROM doctor_results WHERE learner_id = @learner_id AND check_id = @check_id AND source = 'learner';
