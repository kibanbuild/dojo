-- name: InsertSession :one
INSERT INTO sessions (org_id, learner_id, token_hash)
VALUES ($1, $2, $3)
RETURNING id, created_at;

-- Active = not revoked and used within the 30-day sliding window (design 3.1).
-- name: GetActiveSession :one
SELECT s.id, s.learner_id, s.last_used_at, l.suspended_at
FROM sessions s
JOIN learners l ON l.id = s.learner_id
WHERE s.token_hash = $1
  AND s.revoked_at IS NULL
  AND s.last_used_at > now() - interval '30 days';

-- name: TouchSession :exec
UPDATE sessions SET last_used_at = now(), updated_at = now()
WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = now(), updated_at = now()
WHERE id = $1 AND revoked_at IS NULL;
