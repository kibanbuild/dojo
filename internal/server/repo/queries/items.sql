-- GET /v1/items/{id} (design 3.10): quiz and story items.

-- name: GetItemView :one
SELECT i.id, r.slug AS role, i.kind, i.title, i.est_minutes, i.tags, i.payload
FROM catalog_items i
JOIN roles r ON r.id = i.role_id
WHERE i.id = $1 AND i.kind IN ('quiz', 'story');

-- name: ItemIsDone :one
SELECT EXISTS (SELECT 1 FROM item_completions WHERE learner_id = $1 AND item_id = $2) AS done;

-- name: ListQuizAnswers :many
SELECT question_id, first_choice, first_correct, last_choice, last_correct
FROM quiz_answers
WHERE learner_id = $1 AND item_id = $2;

-- name: GetStoryChoice :one
SELECT choice_id, chosen_at FROM story_choices WHERE learner_id = $1 AND item_id = $2;
