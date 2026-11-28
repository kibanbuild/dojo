//go:build integration

package server_test

import (
	"strconv"
	"strings"
	"testing"
)

const (
	ctVersion = "0123456789ab"
	ctSHA     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func catalogFixtureCleanup(t *testing.T) {
	t.Helper()
	clean := func() {
		asOwner(t, "DELETE FROM catalog_items WHERE id LIKE 'ct-%'")
		asOwner(t, "DELETE FROM path_roles WHERE path_id IN (SELECT id FROM paths WHERE slug LIKE 'ct-%')")
		asOwner(t, "DELETE FROM paths WHERE slug LIKE 'ct-%'")
		asOwner(t, "DELETE FROM roles WHERE slug LIKE 'ct-%'")
		asOwner(t, "DELETE FROM catalog_state")
	}
	clean()
	t.Cleanup(clean)
}

// rejects runs sql as the migration role and wants the named constraint to refuse it.
func rejects(t *testing.T, name, sql, constraint string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		out, err := psqlAs(t, "dojo", envExample(t, "POSTGRES_PASSWORD"), sql)
		if err == nil {
			t.Fatalf("want a violation of %s, the statement succeeded", constraint)
		}
		if !strings.Contains(out, constraint) {
			t.Fatalf("want a violation of %s, got %q", constraint, out)
		}
	})
}

func item(id, extra string) string {
	// kind practice is complete like a ticket (00025): the brief, hints and bundle columns come from base.
	return "INSERT INTO catalog_items (id, org_id, kind, position, title, est_minutes, catalog_version, brief, hints, bundle_key, bundle_sha256, bundle_bytes" + extra
}

func TestCatalogItemChecks(t *testing.T) {
	catalogFixtureCleanup(t)
	const base = ", 'brief', ARRAY['h'], 'bundles/practice/x/x.tar.gz', '" + ctSHA + "', 100 FROM orgs WHERE slug = 'public'"
	ok := item("x", ") SELECT 'ct-ok/one', id, 'practice', 1, 'T', 10, '"+ctVersion+"'"+base)
	asOwner(t, ok)

	rejects(t, "est_minutes over 240", item("x", ") SELECT 'ct-a/b', id, 'practice', 2, 'T', 241, '"+ctVersion+"'"+base), "est_minutes")
	rejects(t, "title over 80", item("x", ") SELECT 'ct-a/b', id, 'practice', 2, '"+strings.Repeat("t", 81)+"', 1, '"+ctVersion+"'"+base), "title")
	rejects(t, "bad kind", item("x", ") SELECT 'ct-a/b', id, 'exam', 2, 'T', 1, '"+ctVersion+"'"+base), "kind")
	rejects(t, "bad id", item("x", ") SELECT 'CT/Bad', id, 'practice', 2, 'T', 1, '"+ctVersion+"'"+base), "catalog_items_id_check")
	rejects(t, "catalog_version not 12 hex", item("x", ") SELECT 'ct-a/b', id, 'practice', 2, 'T', 1, 'abc'"+base), "catalog_version")

	set := func(col, val string) string {
		return "UPDATE catalog_items SET " + col + " = " + val + " WHERE id = 'ct-ok/one'"
	}
	rejects(t, "hints empty", set("hints", "'{}'"), "catalog_items_hints_check")
	rejects(t, "hints four", set("hints", "ARRAY['a','b','c','d']"), "catalog_items_hints_check")
	rejects(t, "hint empty string", set("hints", "ARRAY['a','']"), "catalog_items_hints_check")
	rejects(t, "hint over 1000", set("hints", "ARRAY['"+strings.Repeat("h", 1001)+"']"), "catalog_items_hints_check")
	rejects(t, "hint null element", set("hints", "ARRAY['a',NULL]"), "catalog_items_hints_check")
	rejects(t, "hints two dimensions", set("hints", "ARRAY[['a','b'],['c','d']]"), "catalog_items_hints_check")
	rejects(t, "tags two dimensions", set("tags", "ARRAY[['a','b'],['c','d']]"), "catalog_items_tags_check")
	asOwner(t, set("hints", "ARRAY['a','"+strings.Repeat("h", 1000)+"','c']"))
	rejects(t, "tags nine", set("tags", "ARRAY['a','b','c','d','e','f','g','h','i']"), "catalog_items_tags_check")
	rejects(t, "tags duplicate", set("tags", "ARRAY['a','a']"), "catalog_items_tags_check")
	rejects(t, "tag uppercase", set("tags", "ARRAY['A']"), "catalog_items_tags_check")
	rejects(t, "tag over 24", set("tags", "ARRAY['"+strings.Repeat("a", 25)+"']"), "catalog_items_tags_check")
	asOwner(t, set("tags", "ARRAY['linux','perms-2']"))
	asOwner(t, set("tags", "'{}'"))
	rejects(t, "brief empty", set("brief", "''"), "catalog_items_brief_check")
	rejects(t, "brief over 8192 bytes", set("brief", "'"+strings.Repeat("b", 8193)+"'"), "catalog_items_brief_check")
	asOwner(t, set("brief", "'"+strings.Repeat("b", 8192)+"'"))
	bundle := func(sha string, n int) string {
		return "UPDATE catalog_items SET bundle_key = 'bundles/a/b/x.tar.gz', bundle_sha256 = '" + sha +
			"', bundle_bytes = " + strconv.Itoa(n) + " WHERE id = 'ct-ok/one'"
	}
	rejects(t, "sha too short", bundle("abc", 1), "bundle_sha256")
	rejects(t, "sha uppercase", bundle(strings.ToUpper(ctSHA), 1), "bundle_sha256")
	rejects(t, "bundle over 1 MiB", bundle(ctSHA, 1048577), "bundle_bytes")
	rejects(t, "bundle empty", bundle(ctSHA, 0), "bundle_bytes")
	rejects(t, "bundle columns all or none", set("bundle_key", "NULL"), "catalog_items_bundle_all_or_none")
	asOwner(t, "UPDATE catalog_items SET bundle_key = 'bundles/a/b/x.tar.gz', bundle_sha256 = '"+ctSHA+"', bundle_bytes = 1048576 WHERE id = 'ct-ok/one'")
	rejects(t, "ticket without role", "UPDATE catalog_items SET kind = 'ticket' WHERE id = 'ct-ok/one'", "catalog_items_role_kind")
}

func TestCatalogKindChecks(t *testing.T) {
	catalogFixtureCleanup(t)
	asOwner(t, "DELETE FROM belt_marks WHERE slug LIKE 'ct-%'")
	t.Cleanup(func() {
		asOwner(t, "DELETE FROM catalog_items WHERE id LIKE 'ct-%'")
		asOwner(t, "DELETE FROM belt_marks WHERE slug LIKE 'ct-%'")
	})
	const org = " FROM orgs WHERE slug = 'public'"
	asOwner(t, "INSERT INTO belt_marks (org_id, slug, belt, title, summary, position) SELECT id, 'ct-mark', 'white', 'M', 'S', 1"+org)
	asOwner(t, "INSERT INTO roles (org_id, slug, title, kind, has_exam) SELECT id, 'ct-role', 'R', 'role', true"+org)
	pos := 0
	// kind is a column, not a literal, so the same statement shape serves every kind.
	row := func(id, kind, role, cols, vals string) string {
		pos++
		return "INSERT INTO catalog_items (id, org_id, role_id, kind, position, title, est_minutes, catalog_version" + cols + ") " +
			"SELECT '" + id + "', o.id, " + role + ", '" + kind + "', " + strconv.Itoa(pos) + ", 'T', 1, '" + ctVersion + "'" + vals + " FROM orgs o LEFT JOIN roles r ON r.slug = 'ct-role' WHERE o.slug = 'public'"
	}
	const rid = "r.id"
	asOwner(t, row("ct-role/w", "writing", rid, ", payload, awards_mark", ", '{\"a\":1}'::jsonb, 'ct-mark'"))
	asOwner(t, row("ct-role/q", "quiz", rid, ", payload", ", '{}'::jsonb"))
	rejects(t, "writing without payload", row("ct-role/x", "writing", rid, "", ""), "catalog_items_payload_kind")
	rejects(t, "quiz without payload", row("ct-role/x", "quiz", rid, "", ""), "catalog_items_payload_kind")
	rejects(t, "payload on a ticket", row("ct-role/x", "ticket", rid, ", payload, brief, hints, bundle_key, bundle_sha256, bundle_bytes",
		", '{}'::jsonb, 'b', ARRAY['h'], 'k', '"+ctSHA+"', 1"), "catalog_items_payload_kind")
	rejects(t, "payload not an object", row("ct-role/x", "story", rid, ", payload", ", '[]'::jsonb"), "catalog_items_payload_check")
	rejects(t, "payload over 64 KB", row("ct-role/x", "story", rid, ", payload", ", jsonb_build_object('a', repeat('x', 65536))"), "catalog_items_payload_check")
	asOwner(t, row("ct-role/big", "story", rid, ", payload", ", jsonb_build_object('a', repeat('x', 65000))"))
	rejects(t, "awards_mark on a quiz", row("ct-role/x", "quiz", rid, ", payload, awards_mark", ", '{}'::jsonb, 'ct-mark'"), "catalog_items_awards_mark_kind")
	rejects(t, "awards_mark names no mark", row("ct-role/x", "writing", rid, ", payload, awards_mark", ", '{}'::jsonb, 'ct-ghost'"), "catalog_items_awards_mark_fkey")
	rejects(t, "difficulty on a quiz", row("ct-role/x", "quiz", rid, ", payload, difficulty", ", '{}'::jsonb, 'easy'"), "catalog_items_difficulty_kind")
	rejects(t, "practice with a role", row("ct-p/one", "practice", rid, ", brief, hints, bundle_key, bundle_sha256, bundle_bytes",
		", 'b', ARRAY['h'], 'k', '"+ctSHA+"', 1"), "catalog_items_role_kind")
	rejects(t, "ticket without a role", row("ct-role/x", "ticket", "NULL", ", brief, hints, bundle_key, bundle_sha256, bundle_bytes",
		", 'b', ARRAY['h'], 'k', '"+ctSHA+"', 1"), "catalog_items_role_kind")
	rejects(t, "quiz without a role", row("ct-role/x", "quiz", "NULL", ", payload", ", '{}'::jsonb"), "catalog_items_role_kind")
	rejects(t, "practice without bundle", row("ct-p/two", "practice", "NULL", ", brief, hints", ", 'b', ARRAY['h']"), "catalog_items_practice_complete")
	rejects(t, "practice without brief", row("ct-p/two", "practice", "NULL", ", hints, bundle_key, bundle_sha256, bundle_bytes",
		", ARRAY['h'], 'k', '"+ctSHA+"', 1"), "catalog_items_practice_complete")
	for _, d := range []string{"easy", "medium", "hard"} {
		asOwner(t, row("ct-p/"+d, "practice", "NULL", ", brief, hints, bundle_key, bundle_sha256, bundle_bytes, difficulty",
			", 'b', ARRAY['h'], 'k', '"+ctSHA+"', 1, '"+d+"'"))
	}
	rejects(t, "difficulty unknown", "UPDATE catalog_items SET difficulty = 'insane' WHERE id = 'ct-p/easy'", "catalog_items_difficulty_check")
	rejects(t, "a mark cannot be dropped while an item awards it", "DELETE FROM belt_marks WHERE slug = 'ct-mark'", "catalog_items_awards_mark_fkey")
}

func TestBeltMarkChecks(t *testing.T) {
	t.Cleanup(func() { asOwner(t, "DELETE FROM belt_marks WHERE slug LIKE 'ct-%'") })
	asOwner(t, "DELETE FROM belt_marks WHERE slug LIKE 'ct-%'")
	mark := func(slug, belt, title, summary string, pos int) string {
		return "INSERT INTO belt_marks (org_id, slug, belt, title, summary, position) SELECT id, '" + slug + "', '" + belt + "', '" + title +
			"', '" + summary + "', " + strconv.Itoa(pos) + " FROM orgs WHERE slug = 'public'"
	}
	asOwner(t, mark("ct-one", "white", "T", "S", 1))
	rejects(t, "slug unique", mark("ct-one", "yellow", "T", "S", 1), "belt_marks_slug_key")
	rejects(t, "belt unknown", mark("ct-two", "purple", "T", "S", 2), "belt_marks_belt_check")
	rejects(t, "slug uppercase", mark("CT-two", "white", "T", "S", 2), "belt_marks_slug_check")
	rejects(t, "title empty", mark("ct-two", "white", "", "S", 2), "belt_marks_title_check")
	rejects(t, "title over 80", mark("ct-two", "white", strings.Repeat("t", 81), "S", 2), "belt_marks_title_check")
	rejects(t, "summary empty", mark("ct-two", "white", "T", "", 2), "belt_marks_summary_check")
	rejects(t, "summary over 200", mark("ct-two", "white", "T", strings.Repeat("s", 201), 2), "belt_marks_summary_check")
	rejects(t, "position zero", mark("ct-two", "white", "T", "S", 0), "belt_marks_position_check")
	rejects(t, "position unique within a belt", mark("ct-two", "white", "T", "S", 1), "belt_marks_belt_position_key")
	asOwner(t, mark("ct-two", "yellow", "T", "S", 1))
	asOwner(t, mark("ct-three", "white", strings.Repeat("t", 80), strings.Repeat("s", 200), 2))
	// Deferred: swapping positions inside one load transaction is allowed.
	asOwner(t, "BEGIN; UPDATE belt_marks SET position = 2 WHERE slug = 'ct-one'; UPDATE belt_marks SET position = 1 WHERE slug = 'ct-three'; COMMIT")
}

func TestCatalogTicketAndPositionRules(t *testing.T) {
	catalogFixtureCleanup(t)
	asOwner(t, "INSERT INTO roles (org_id, slug, title, kind, has_exam) SELECT id, 'ct-role', 'R', 'role', true FROM orgs WHERE slug = 'public'")
	ticket := func(id string, pos int) string {
		return "INSERT INTO catalog_items (id, org_id, role_id, kind, position, title, est_minutes, catalog_version, brief, hints, bundle_key, bundle_sha256, bundle_bytes) " +
			"SELECT '" + id + "', o.id, r.id, 'ticket', " + string(rune('0'+pos)) + ", 'T', 5, '" + ctVersion + "', 'brief', ARRAY['h'], 'bundles/ct-role/t/x.tar.gz', '" + ctSHA + "', 100 " +
			"FROM orgs o, roles r WHERE o.slug = 'public' AND r.slug = 'ct-role'"
	}
	rejects(t, "ticket id with three parts", ticket("ct-role/one/x", 3), "catalog_items_ticket_id_shape")
	rejects(t, "ticket id with one part", ticket("ct-role-one", 3), "catalog_items_ticket_id_shape")
	asOwner(t, ticket("ct-role/one", 1))
	rejects(t, "position unique within a role", ticket("ct-role/two", 1), "catalog_items_role_position_key")
	asOwner(t, ticket("ct-role/two", 2))
	// Deferred: swapping positions inside one load transaction is allowed.
	asOwner(t, "BEGIN; UPDATE catalog_items SET position = 2 WHERE id = 'ct-role/one'; UPDATE catalog_items SET position = 1 WHERE id = 'ct-role/two'; COMMIT")
	if got := asOwner(t, "SELECT position FROM catalog_items WHERE id = 'ct-role/one'"); got != "2" {
		t.Fatalf("position after swap: %q", got)
	}
	rejects(t, "role must exist", "INSERT INTO catalog_items (id, org_id, role_id, kind, position, title, est_minutes, catalog_version, brief, hints, bundle_key, bundle_sha256, bundle_bytes) "+
		"SELECT 'ct-role/ghost', id, gen_random_uuid(), 'ticket', 9, 'T', 1, '"+ctVersion+"', 'brief', ARRAY['h'], 'bundles/ct-role/t/x.tar.gz', '"+ctSHA+"', 100 FROM orgs WHERE slug = 'public'", "catalog_items_role_id_fkey")
}

func TestCatalogPathAndRoleChecks(t *testing.T) {
	catalogFixtureCleanup(t)
	const org = " FROM orgs WHERE slug = 'public'"
	path := func(slug, status string, pos int) string {
		return "INSERT INTO paths (org_id, slug, title, status, position) SELECT id, '" + slug + "', 'P', '" + status + "', " + string(rune('0'+pos)) + org
	}
	asOwner(t, path("ct-p1", "available", 1))
	rejects(t, "path status", path("ct-p2", "live", 2), "paths_status_check")
	rejects(t, "path slug unique", path("ct-p1", "planned", 2), "paths_slug_key")
	rejects(t, "path position unique", path("ct-p2", "planned", 1), "paths_position_key")
	rejects(t, "path slug format", path("CT_P", "planned", 3), "paths_slug_check")

	role := func(slug, kind string) string {
		return "INSERT INTO roles (org_id, slug, title, kind, has_exam) SELECT id, '" + slug + "', 'R', '" + kind + "', false" + org
	}
	asOwner(t, role("ct-r1", "foundations"))
	asOwner(t, role("ct-r2", "role"))
	rejects(t, "role kind", role("ct-r3", "track"), "roles_kind_check")
	rejects(t, "role slug unique", role("ct-r1", "role"), "roles_slug_key")
	if got := asOwner(t, "SELECT advanced FROM roles WHERE slug = 'ct-r1'"); got != "f" {
		t.Fatalf("advanced default: %q, want false", got)
	}

	if got := asOwner(t, "SELECT summary FROM roles WHERE slug = 'ct-r1'"); got != "" {
		t.Fatalf("summary default: %q, want empty", got)
	}
	rejects(t, "role summary length", "INSERT INTO roles (org_id, slug, title, summary, kind, has_exam) SELECT id, 'ct-r4', 'R', repeat('a', 121), 'role', false"+org, "roles_summary_len")
	asOwner(t, "INSERT INTO roles (org_id, slug, title, summary, kind, has_exam) SELECT id, 'ct-r5', 'R', repeat('a', 120), 'role', false"+org)

	link := func(r string, pos int) string {
		return "INSERT INTO path_roles (org_id, path_id, role_id, position) SELECT o.id, p.id, r.id, " + string(rune('0'+pos)) +
			" FROM orgs o, paths p, roles r WHERE o.slug = 'public' AND p.slug = 'ct-p1' AND r.slug = '" + r + "'"
	}
	asOwner(t, link("ct-r1", 1))
	rejects(t, "role twice in a path", link("ct-r1", 2), "path_roles_path_role_key")
	rejects(t, "position twice in a path", link("ct-r2", 1), "path_roles_path_position_key")
	asOwner(t, link("ct-r2", 2))
}

func TestCatalogStateIsOneRow(t *testing.T) {
	catalogFixtureCleanup(t)
	const org = " FROM orgs WHERE slug = 'public'"
	asOwner(t, "INSERT INTO catalog_state (id, org_id, catalog_version) SELECT 1, id, '"+ctVersion+"'"+org)
	rejects(t, "second row", "INSERT INTO catalog_state (id, org_id, catalog_version) SELECT 2, id, '"+ctVersion+"'"+org, "catalog_state_id_check")
	rejects(t, "same id twice", "INSERT INTO catalog_state (id, org_id, catalog_version) SELECT 1, id, '"+ctVersion+"'"+org, "catalog_state_pkey")
	rejects(t, "version format", "UPDATE catalog_state SET catalog_version = 'XYZ' WHERE id = 1", "catalog_version")
	// The load upserts on id = 1.
	asOwner(t, "INSERT INTO catalog_state (id, org_id, catalog_version) SELECT 1, id, 'ba9876543210'"+org+
		" ON CONFLICT (id) DO UPDATE SET catalog_version = EXCLUDED.catalog_version, updated_at = now()")
	if got := asOwner(t, "SELECT catalog_version FROM catalog_state"); got != "ba9876543210" {
		t.Fatalf("version: %q", got)
	}
}
