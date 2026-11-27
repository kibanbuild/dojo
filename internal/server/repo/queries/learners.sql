-- A sign-in refresh. A GitHub email replaces the stored one and marks it as GitHub's; with none the stored one stays.
-- A learner who had no address before and now has one gets E1 in the same statement (prev reads the row as it was
-- before this statement), once however many triggers fire (ON CONFLICT on kind and ref).
-- name: UpsertLearnerWithWelcome :one
WITH prev AS (
    SELECT l.id, l.email FROM learners l WHERE l.github_id = $2
), up AS (
    INSERT INTO learners (org_id, github_id, github_login, username, email, email_source, language,
                          age_confirmed_at, terms_accepted_at, terms_version, usage_stats)
    VALUES ($1, $2, $3, $4, $5, CASE WHEN $5::text IS NULL THEN NULL ELSE 'github' END, $6, $7, $8, $9, $10)
    ON CONFLICT (github_id) DO UPDATE
    SET github_login = EXCLUDED.github_login,
        email = COALESCE(EXCLUDED.email, learners.email),
        email_source = CASE WHEN EXCLUDED.email IS NULL THEN learners.email_source ELSE 'github' END,
        updated_at = now()
    RETURNING *
), queued AS (
    INSERT INTO email_outbox (learner_id, to_address, kind, ref)
    SELECT up.id, up.email, 'E1', up.id::text FROM up JOIN prev ON prev.id = up.id
    WHERE prev.email IS NULL AND up.email IS NOT NULL
    ON CONFLICT (kind, ref) DO NOTHING
)
SELECT * FROM up;

-- A new learner and their welcome mail (E1, ref = the learner id) in one statement, so both exist or
-- neither does. No row back means the GitHub account already had a learner (a concurrent first sign-in).
-- name: InsertLearnerWithWelcome :one
WITH ins AS (
    INSERT INTO learners (org_id, github_id, github_login, username, email, email_source, language,
                          age_confirmed_at, terms_accepted_at, terms_version, usage_stats)
    VALUES ($1, $2, $3, $4, $5, CASE WHEN $5::text IS NULL THEN NULL ELSE 'github' END, $6, $7, $8, $9, $10)
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
    hosted_default = COALESCE(sqlc.narg('hosted_default'), hosted_default),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- The learner's own address (design 3.9), only while none is on file, with E1 queued in the same statement.
-- No row back means an address is already stored.
-- name: SetLearnerEmailWithWelcome :one
WITH upd AS (
    UPDATE learners l
    SET email = sqlc.arg('email')::text, email_source = 'learner', updated_at = now()
    WHERE l.id = sqlc.arg('id')::uuid AND l.email IS NULL
    RETURNING *
), queued AS (
    INSERT INTO email_outbox (learner_id, to_address, kind, ref)
    SELECT upd.id, upd.email, 'E1', upd.id::text FROM upd
    ON CONFLICT (kind, ref) DO NOTHING
)
SELECT * FROM upd;

-- No row back means the first run was already seen: a repeat changes nothing.
-- name: MarkFirstRunSeen :one
UPDATE learners SET first_run_seen_at = now(), updated_at = now()
WHERE id = $1 AND first_run_seen_at IS NULL
RETURNING *;

-- The learner's earliest first pass while the first run is unseen (design 3.9); no row means no first run.
-- name: GetLearnerFirstRun :one
SELECT p.item_id, i.title, p.first_passed_at, p.commit_state
FROM ticket_progress p
JOIN catalog_items i ON i.id = p.item_id AND i.kind = 'ticket'
JOIN learners l ON l.id = p.learner_id
WHERE p.learner_id = $1 AND p.state IN ('passed', 'revisit') AND l.first_run_seen_at IS NULL
ORDER BY p.first_passed_at, p.item_id
LIMIT 1;

-- name: SetLearnerAnalyticsConsentIfUnset :execrows
UPDATE learners
SET analytics_consent = $2, updated_at = now()
WHERE id = $1 AND analytics_consent IS NULL;

-- Erases one learner through the SECURITY DEFINER function (design 3.5); false means no such learner.
-- name: DeleteLearner :one
SELECT delete_learner(sqlc.arg('id')::uuid)::boolean AS deleted;
