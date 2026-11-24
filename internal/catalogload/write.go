package catalogload

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/kibanbuild/dojo/internal/catalog"
)

// write upserts the catalog in tx, removes what the catalog dropped and refuses (an error, so the
// caller rolls back) when a dropped item has progress.
func write(ctx context.Context, tx pgx.Tx, cat *catalog.Catalog, bundles []*catalog.Bundle, version string) error {
	var org string
	if err := tx.QueryRow(ctx, `SELECT id FROM orgs WHERE slug = 'public'`).Scan(&org); err != nil {
		return fmt.Errorf("find org: %w", err)
	}
	exec := func(sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("%s: %w", strings.Fields(sql)[0]+" "+strings.Fields(sql)[2], err)
		}
		return nil
	}

	ids := make([]string, len(cat.Tickets)) // never nil: an empty catalog must not send NULL
	for i, t := range cat.Tickets {
		ids[i] = t.ID
	}
	// Lock the rows the catalog drops, then look for progress on them: a first start racing this load
	// either lands before the lock (and is seen) or waits for the commit and fails its foreign key.
	rows, err := tx.Query(ctx, `
		SELECT c.id FROM catalog_items c
		WHERE c.kind = 'ticket' AND NOT (c.id = ANY($1)) FOR UPDATE`, ids)
	if err != nil {
		return fmt.Errorf("lock dropped items: %w", err)
	}
	dropped, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("lock dropped items: %w", err)
	}
	if len(dropped) > 0 {
		var blocked []string
		rows, err := tx.Query(ctx, `
			SELECT c.id FROM catalog_items c
			WHERE c.id = ANY($1) AND NOT (
				NOT EXISTS (SELECT 1 FROM ticket_progress p WHERE p.item_id = c.id)
				AND NOT EXISTS (SELECT 1 FROM hint_opens h WHERE h.item_id = c.id))
			ORDER BY c.id`, dropped)
		if err != nil {
			return fmt.Errorf("check progress: %w", err)
		}
		if blocked, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return fmt.Errorf("check progress: %w", err)
		}
		if len(blocked) > 0 {
			return fmt.Errorf("%w: %s", ErrWouldDropProgress, strings.Join(blocked, ", "))
		}
		if err := exec(`DELETE FROM catalog_items WHERE id = ANY($1)`, dropped); err != nil {
			return err
		}
	}

	pathSlugs, roleSlugs := []string{}, []string{}
	for _, p := range cat.Paths {
		pathSlugs = append(pathSlugs, p.Slug)
		if err := exec(`INSERT INTO paths (org_id, slug, title, status, position) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (slug) DO UPDATE SET title = EXCLUDED.title, status = EXCLUDED.status,
			position = EXCLUDED.position, updated_at = now()`, org, p.Slug, p.Title, p.Status, p.Position); err != nil {
			return err
		}
	}
	for _, r := range cat.Roles {
		roleSlugs = append(roleSlugs, r.Slug)
		if err := exec(`INSERT INTO roles (org_id, slug, title, kind, has_exam, advanced) VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (slug) DO UPDATE SET title = EXCLUDED.title, kind = EXCLUDED.kind,
			has_exam = EXCLUDED.has_exam, advanced = EXCLUDED.advanced, updated_at = now()`,
			org, r.Slug, r.Title, r.Kind, r.HasExam, r.Advanced); err != nil {
			return err
		}
	}
	for _, pr := range cat.PathRoles {
		if err := exec(`INSERT INTO path_roles (org_id, path_id, role_id, position)
			SELECT $1, p.id, r.id, $4 FROM paths p, roles r WHERE p.slug = $2 AND r.slug = $3
			ON CONFLICT (path_id, role_id) DO UPDATE SET position = EXCLUDED.position, updated_at = now()`,
			org, pr.Path, pr.Role, pr.Position); err != nil {
			return err
		}
	}
	for i, t := range cat.Tickets {
		b := bundles[i]
		if err := exec(`INSERT INTO catalog_items (id, org_id, role_id, kind, position, title, est_minutes, tags, brief, hints,
				catalog_version, bundle_key, bundle_sha256, bundle_bytes)
			SELECT $1, $2, r.id, 'ticket', $4, $5, $6, $7, $8, $9, $10, $11, $12, $13 FROM roles r WHERE r.slug = $3
			ON CONFLICT (id) DO UPDATE SET role_id = EXCLUDED.role_id, position = EXCLUDED.position,
				title = EXCLUDED.title, est_minutes = EXCLUDED.est_minutes, tags = EXCLUDED.tags,
				brief = EXCLUDED.brief, hints = EXCLUDED.hints, catalog_version = EXCLUDED.catalog_version,
				bundle_key = EXCLUDED.bundle_key, bundle_sha256 = EXCLUDED.bundle_sha256,
				bundle_bytes = EXCLUDED.bundle_bytes, updated_at = now()`,
			t.ID, org, t.Role, t.Order, t.Title, t.EstMinutes, nonNil(t.Tags), t.Brief, t.Hints,
			version, b.Key(), b.SHA256, len(b.Data)); err != nil {
			return err
		}
	}
	// Memberships the catalog no longer lists, then the paths and roles it dropped, after the items
	// moved off them. A role that still has items fails its foreign key and rolls the load back.
	if err := exec(`DELETE FROM path_roles pr USING paths p, roles r
		WHERE pr.path_id = p.id AND pr.role_id = r.id
		AND NOT ((p.slug, r.slug) IN (SELECT * FROM unnest($1::text[], $2::text[])))`,
		pairs(cat, true), pairs(cat, false)); err != nil {
		return err
	}
	if err := exec(`DELETE FROM paths WHERE NOT (slug = ANY($1))`, pathSlugs); err != nil {
		return err
	}
	if err := exec(`DELETE FROM roles WHERE NOT (slug = ANY($1))`, roleSlugs); err != nil {
		return err
	}
	return exec(`INSERT INTO catalog_state (id, org_id, catalog_version) VALUES (1, $1, $2)
		ON CONFLICT (id) DO UPDATE SET catalog_version = EXCLUDED.catalog_version, updated_at = now()`, org, version)
}

func pairs(cat *catalog.Catalog, path bool) []string {
	out := make([]string, len(cat.PathRoles))
	for i, pr := range cat.PathRoles {
		out[i] = pr.Role
		if path {
			out[i] = pr.Path
		}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
