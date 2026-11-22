-- name: UpsertLearner :one
INSERT INTO learners (org_id, github_id, github_login, username, email, language,
                      age_confirmed_at, terms_accepted_at, terms_version, usage_stats)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (github_id) DO UPDATE
SET github_login = EXCLUDED.github_login,
    email = COALESCE(EXCLUDED.email, learners.email),
    updated_at = now()
RETURNING *;

-- name: GetLearnerByID :one
SELECT * FROM learners WHERE id = $1;

-- name: GetLearnerByGithubID :one
SELECT * FROM learners WHERE github_id = $1;

-- name: UpdateLearnerProfile :one
UPDATE learners
SET username = COALESCE(sqlc.narg('username'), username),
    language = COALESCE(sqlc.narg('language'), language),
    updated_at = now()
WHERE id = $1
RETURNING *;
