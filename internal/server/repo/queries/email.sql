-- Unsent rows whose template exists, minus the ids in skip (rows backing off after a failed send, so they never
-- fill the batch and starve the rows behind them). Rows with no address (GitHub gave none) are skipped, and so are rows
-- whose learner is gone (only E8 is sent with no learner; delete_learner drops the rest). The sender
-- claims one row at a time (EmailTryLock), so this read takes no lock.
-- name: ListUnsentEmails :many
SELECT id, learner_id, kind FROM email_outbox
WHERE sent_at IS NULL AND to_address IS NOT NULL AND (learner_id IS NOT NULL OR kind = 'E8') AND kind = ANY(sqlc.arg('kinds')::text[])
  AND id <> ALL(COALESCE(sqlc.arg('skip')::uuid[], '{}'))
ORDER BY created_at
LIMIT sqlc.arg('max')::int;

-- dojo_api has no UPDATE, so SELECT ... FOR UPDATE is not open to it. A transaction-scoped advisory lock
-- on the row id keeps two API tasks from sending the same mail; a task that loses the race skips the row.
-- name: EmailTryLock :one
SELECT pg_try_advisory_xact_lock(sqlc.arg('class')::int, hashtext(sqlc.arg('id')::uuid::text))::boolean AS locked;

-- Re-read under the lock: another task may have sent it between the list and the lock.
-- name: GetEmailForSend :one
SELECT id, learner_id, kind, to_address FROM email_outbox
WHERE id = $1 AND sent_at IS NULL AND to_address IS NOT NULL;

-- name: MarkEmailSent :one
SELECT mark_email_sent(sqlc.arg('id')::uuid)::boolean AS marked;
