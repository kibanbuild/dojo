-- name: UpsertLearner :one
INSERT INTO learners (org_id, github_id, github_login, username, email, language,
                      age_confirmed_at, terms_accepted_at, terms_version, usage_stats)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (github_id) DO UPDATE
SET github_login = EXCLUDED.github_login,
    email = COALESCE(EXCLUDED.email, learners.email),
    updated_at = now()
RETURNING *;

-- A new learner and their welcome mail (E1, ref = the learner id) in one statement, so both exist or
-- neither does. No row back means the GitHub account already had a learner (a concurrent first sign-in).
-- name: InsertLearnerWithWelcome :one
WITH ins AS (
    INSERT INTO learners (org_id, github_id, github_login, username, email, language,
                          age_confirmed_at, terms_accepted_at, terms_version, usage_stats)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
    ON CONFLICT (github_id) DO NOTHING
    RETURNING *
), queued AS (
    INSERT INTO email_outbox (learner_id, to_address, kind, ref)
    SELECT id, email, 'E1', id::text FROM ins WHERE email IS NOT NULL
    ON CONFLICT (kind, ref) DO NOTHING
)
SELECT * FROM ins;

-- name: GetLearnerByID :one
SELECT * FROM learners WHERE id = $1;

-- name: GetLearnerByGithubID :one
SELECT * FROM learners WHERE github_id = $1;

-- name: UpdateLearnerProfile :one
UPDATE learners
SET username = COALESCE(sqlc.narg('username'), username),
    language = COALESCE(sqlc.narg('language'), language),
    usage_stats = COALESCE(sqlc.narg('usage_stats'), usage_stats),
    analytics_consent = COALESCE(sqlc.narg('analytics_consent'), analytics_consent),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetLearnerAnalyticsConsentIfUnset :execrows
UPDATE learners
SET analytics_consent = $2, updated_at = now()
WHERE id = $1 AND analytics_consent IS NULL;

-- Erases one learner through the SECURITY DEFINER function (design 3.5); false means no such learner.
-- name: DeleteLearner :one
SELECT delete_learner(sqlc.arg('id')::uuid)::boolean AS deleted;
