-- name: GetOrgBySlug :one
SELECT id, slug, name, created_at, updated_at FROM orgs WHERE slug = $1;
