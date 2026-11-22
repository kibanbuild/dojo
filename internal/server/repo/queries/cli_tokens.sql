-- name: InsertCLIToken :one
INSERT INTO cli_tokens (org_id, learner_id, token_hash, device_name, os, cli_version)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, created_at;

-- Active = not revoked. A CLI token has no expiry (design 3.1); last_used_at only feeds S2.
-- name: GetActiveCLIToken :one
SELECT t.id, t.learner_id, t.last_used_at, l.suspended_at
FROM cli_tokens t
JOIN learners l ON l.id = t.learner_id
WHERE t.token_hash = $1
  AND t.revoked_at IS NULL;

-- name: TouchCLIToken :exec
UPDATE cli_tokens SET last_used_at = now(), updated_at = now()
WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeCLIToken :exec
UPDATE cli_tokens SET revoked_at = now(), updated_at = now()
WHERE id = $1 AND revoked_at IS NULL;
