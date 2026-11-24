-- name: GetCatalogVersion :one
SELECT catalog_version FROM catalog_state WHERE id = 1;

-- name: ListCatalogPaths :many
SELECT slug, title, status, position FROM paths ORDER BY position;

-- name: ListCatalogRoles :many
SELECT slug, title, kind, has_exam, advanced FROM roles ORDER BY slug;

-- name: ListCatalogPathRoles :many
SELECT p.slug AS path, r.slug AS role, pr.position
FROM path_roles pr
JOIN paths p ON p.id = pr.path_id
JOIN roles r ON r.id = pr.role_id
ORDER BY p.position, pr.position;

-- Items that belong to a role; practice items have no role and are not part of a path.
-- name: ListCatalogItems :many
SELECT i.id, r.slug AS role, i.position, i.kind, i.title, i.est_minutes, i.tags
FROM catalog_items i
JOIN roles r ON r.id = i.role_id
ORDER BY r.slug, i.position;

-- name: ListLearnerTicketStates :many
SELECT item_id, state, lab_state FROM ticket_progress WHERE learner_id = $1;
