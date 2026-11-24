-- +goose Up
-- A2 learner lookup (design 3.7): dojo_admin reads the catalog titles that the progress view joins
-- (roles for the role names and ticket totals, catalog_items for ticket titles), and the three
-- indexes serve the search by username prefix, GitHub login prefix and exact email. The email index
-- is on lower(email): the search is an exact, case-insensitive match, never a substring.
--
-- Grants for both roles, as the design requires of every migration:
--   dojo_api:   unchanged, it already reads the whole catalog (00012).
--   dojo_admin: SELECT on roles and catalog_items only. paths, path_roles and catalog_state stay
--               ungranted; no write on any catalog table.
CREATE INDEX learners_username_prefix_idx ON learners (username text_pattern_ops);
CREATE INDEX learners_github_login_prefix_idx ON learners (lower(github_login) text_pattern_ops);
CREATE INDEX learners_email_lower_idx ON learners (lower(email));

GRANT SELECT ON roles, catalog_items TO dojo_admin;

-- +goose Down
REVOKE SELECT ON roles, catalog_items FROM dojo_admin;
DROP INDEX learners_email_lower_idx;
DROP INDEX learners_github_login_prefix_idx;
DROP INDEX learners_username_prefix_idx;
