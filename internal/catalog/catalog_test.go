package catalog_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kibanbuild/dojo/internal/catalog"
)

const validTicket = `id: support-l1/perms-01
title: Fix it
role: support-l1
order: 1
est_minutes: 20
tags: [a, b]
brief: Do the thing.
hints: [one, two]
`

const validCatalog = `paths:
  - {slug: systems, title: Systems, position: 1, status: available}
roles:
  - {slug: foundations, title: Foundations, kind: foundations, has_exam: false}
  - {slug: support-l1, title: Support L1, kind: role, has_exam: true}
path_roles:
  - {path: systems, role: foundations, position: 1}
  - {path: systems, role: support-l1, position: 2}
`

func fixtureTree() fstest.MapFS {
	return fstest.MapFS{
		"catalog.yaml":                    {Data: []byte(validCatalog)},
		"support-l1/perms-01/ticket.yaml": {Data: []byte(validTicket)},
		"support-l1/perms-01/setup.sh":    {Data: []byte("setup")},
		"support-l1/perms-01/check.sh":    {Data: []byte("check")},
		"foundations/find-01/ticket.yaml": {Data: []byte(replace(validTicket, "support-l1/perms-01", "foundations/find-01", "role: support-l1", "role: foundations"))},
		"foundations/find-01/setup.sh":    {Data: []byte("setup")},
		"foundations/find-01/check.sh":    {Data: []byte("check")},
	}
}

// replace applies old, new pairs in order.
func replace(s string, pairs ...string) string {
	for i := 0; i < len(pairs); i += 2 {
		s = strings.Replace(s, pairs[i], pairs[i+1], 1)
	}
	return s
}

func TestParseTicketErrors(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	tests := []struct {
		name  string
		yaml  string
		field string // must appear in the error, with the file name
	}{
		{"unknown key", replace(validTicket, "hints:", "hint:"), "hint"},
		{"duplicate key", validTicket + "title: again\n", "title"},
		{"empty file", "", "empty"},
		{"two documents", validTicket + "---\n" + validTicket, "more than one"},
		{"not yaml", "id: [unclosed\n", "ticket.yaml"},
		{"merge key", "base: &b {order: 1}\n" + validTicket + "extra:\n  <<: *b\n", "<<"},
		{"float order", replace(validTicket, "order: 1", "order: 1.0"), "order"},
		{"exponent minutes", replace(validTicket, "est_minutes: 20", "est_minutes: 2e1"), "est_minutes"},
		{"float tag", replace(validTicket, "[a, b]", "[1.5]"), "tags[0]"},
		{"wrong type", replace(validTicket, "order: 1", "order: first"), "order"},
		{"id too short", replace(validTicket, "support-l1/perms-01", "a"), "id"},
		{"id too long", replace(validTicket, "support-l1/perms-01", "a/"+long(63)), "id"},
		{"id one part", replace(validTicket, "support-l1/perms-01", "perms01"), "id"},
		{"id upper case", replace(validTicket, "support-l1/perms-01", "Support-l1/perms-01"), "id"},
		{"id double hyphen", replace(validTicket, "support-l1/perms-01", "support--l1/perms-01"), "id"},
		{"id three parts", replace(validTicket, "support-l1/perms-01", "support-l1/perms/01"), "id"},
		{"title missing", replace(validTicket, "title: Fix it\n", ""), "title"},
		{"title too long", replace(validTicket, "Fix it", long(81)), "title"},
		{"title two lines", replace(validTicket, "title: Fix it", `title: "a\nb"`), "title"},
		{"role missing", replace(validTicket, "role: support-l1\n", ""), "role"},
		{"role differs from id", replace(validTicket, "role: support-l1", "role: foundations"), "role"},
		{"role not a slug", replace(validTicket, "role: support-l1", "role: Support"), "role"},
		{"order zero", replace(validTicket, "order: 1", "order: 0"), "order"},
		{"order 1000", replace(validTicket, "order: 1", "order: 1000"), "order"},
		{"minutes zero", replace(validTicket, "est_minutes: 20", "est_minutes: 0"), "est_minutes"},
		{"minutes 241", replace(validTicket, "est_minutes: 20", "est_minutes: 241"), "est_minutes"},
		{"nine tags", replace(validTicket, "[a, b]", "[a, b, c, d, e, f, g, h, i]"), "tags"},
		{"tag upper case", replace(validTicket, "[a, b]", "[A]"), "tags[0]"},
		{"tag too long", replace(validTicket, "[a, b]", "["+long(25)+"]"), "tags[0]"},
		{"tag repeated", replace(validTicket, "[a, b]", "[a, a]"), "tags[1]"},
		{"brief missing", replace(validTicket, "brief: Do the thing.\n", ""), "brief"},
		{"brief blank", replace(validTicket, "Do the thing.", `"  \n"`), "brief"},
		{"brief too big", replace(validTicket, "Do the thing.", long(8193)), "brief"},
		{"no hints", replace(validTicket, "hints: [one, two]", "hints: []"), "hints"},
		{"hints missing", replace(validTicket, "hints: [one, two]\n", ""), "hints"},
		{"four hints", replace(validTicket, "[one, two]", "[a, b, c, d]"), "hints"},
		{"hint empty", replace(validTicket, "[one, two]", `["", b]`), "hints[0]"},
		{"hint too long", replace(validTicket, "[one, two]", "[a, "+long(1001)+"]"), "hints[1]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := catalog.ParseTicket("support-l1/perms-01/ticket.yaml", []byte(tt.yaml))
			if err == nil {
				t.Fatalf("parsed %+v, want an error", got)
			}
			msg := err.Error()
			if !strings.Contains(msg, "support-l1/perms-01/ticket.yaml") || !strings.Contains(msg, tt.field) {
				t.Errorf("error %q must name the file and %q", msg, tt.field)
			}
		})
	}
}

func TestParseTicketBoundaries(t *testing.T) {
	ok := validTicket
	for name, y := range map[string]string{
		"id 64 chars":      replace(ok, "support-l1/perms-01", "support-l1/"+strings.Repeat("a", 53)),
		"title 80 chars":   replace(ok, "Fix it", strings.Repeat("é", 80)),
		"order 999":        replace(ok, "order: 1", "order: 999"),
		"minutes 240":      replace(ok, "est_minutes: 20", "est_minutes: 240"),
		"no tags":          replace(ok, "tags: [a, b]\n", ""),
		"8 tags":           replace(ok, "[a, b]", "[a, b, c, d, e, f, g, h]"),
		"3 hints":          replace(ok, "[one, two]", "[a, b, c]"),
		"brief 8192 bytes": replace(ok, "Do the thing.", strings.Repeat("a", 8192)),
		"hint 1000 chars":  replace(ok, "[one, two]", "["+strings.Repeat("é", 1000)+"]"),
		"tag 24 chars":     replace(ok, "[a, b]", "["+strings.Repeat("a", 24)+"]"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := catalog.ParseTicket("t.yaml", []byte(y)); err != nil {
				t.Errorf("rejected at the limit: %v", err)
			}
		})
	}
}

func TestParseTicketReportsEveryProblem(t *testing.T) {
	_, err := catalog.ParseTicket("t.yaml", []byte(replace(validTicket, "order: 1", "order: 0", "est_minutes: 20", "est_minutes: 0")))
	if err == nil || !strings.Contains(err.Error(), "order") || !strings.Contains(err.Error(), "est_minutes") {
		t.Errorf("want both fields named, got %v", err)
	}
	var ce *catalog.Error
	if !errors.As(err, &ce) || ce.File != "t.yaml" {
		t.Errorf("want a *catalog.Error naming the file, got %T", err)
	}
}

func TestLoadTicket(t *testing.T) {
	tree := fixtureTree()
	tree["support-l1/perms-01/setup.sh"] = &fstest.MapFile{Data: []byte("a\r\nb\r\n")}
	tk, err := catalog.LoadTicket(tree, "support-l1/perms-01")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Title != "Fix it" || string(tk.Setup) != "a\nb\n" || string(tk.Check) != "check" || len(tk.Hints) != 2 {
		t.Errorf("loaded %+v", tk)
	}

	for _, id := range []string{"", "support-l1", "../etc/passwd", "support-l1/perms-01/x", "/support-l1/perms-01", "support-l1/missing"} {
		if _, err := catalog.LoadTicket(tree, id); !errors.Is(err, catalog.ErrNotFound) {
			t.Errorf("LoadTicket(%q) = %v, want ErrNotFound", id, err)
		}
	}

	tree["support-l1/perms-01/ticket.yaml"] = &fstest.MapFile{Data: []byte(replace(validTicket, "perms-01", "perms-02"))}
	if _, err := catalog.LoadTicket(tree, "support-l1/perms-01"); err == nil || errors.Is(err, catalog.ErrNotFound) ||
		!strings.Contains(err.Error(), "support-l1/perms-01/ticket.yaml") || !strings.Contains(err.Error(), "id") {
		t.Errorf("id/dir mismatch: %v", err)
	}

	for _, missing := range []string{"setup.sh", "check.sh"} {
		tree := fixtureTree()
		delete(tree, "support-l1/perms-01/"+missing)
		if _, err := catalog.LoadTicket(tree, "support-l1/perms-01"); err == nil || errors.Is(err, catalog.ErrNotFound) {
			t.Errorf("missing %s: %v", missing, err)
		}
	}
}

func TestParseCatalogErrors(t *testing.T) {
	tests := []struct {
		name, yaml, field string
	}{
		{"unknown key", validCatalog + "extra: 1\n", "extra"},
		{"empty", "", "empty"},
		{"no paths", replace(validCatalog, "paths:\n  - {slug: systems, title: Systems, position: 1, status: available}\n", "paths: []\n"), "paths"},
		{"path slug bad", replace(validCatalog, "slug: systems", "slug: Systems"), "paths[0].slug"},
		{"path status", replace(validCatalog, "status: available", "status: soon"), "paths[0].status"},
		{"path position", replace(validCatalog, "position: 1, status", "position: 0, status"), "paths[0].position"},
		{"path title", replace(validCatalog, "title: Systems", `title: ""`), "paths[0].title"},
		{"role kind", replace(validCatalog, "kind: role", "kind: belt"), "roles[1].kind"},
		{"role duplicate", replace(validCatalog, "slug: support-l1,", "slug: foundations,"), "roles[1].slug"},
		{"role title", replace(validCatalog, "title: Support L1", `title: ""`), "roles[1].title"},
		{"path_role unknown path", replace(validCatalog, "path: systems, role: foundations", "path: nope, role: foundations"), "path_roles[0].path"},
		{"path_role unknown role", replace(validCatalog, "role: support-l1, position", "role: nope, position"), "path_roles[1].role"},
		{"path_role duplicate", replace(validCatalog, "role: support-l1, position: 2", "role: foundations, position: 2"), "path_roles[1]"},
		{"path_role position clash", replace(validCatalog, "role: support-l1, position: 2", "role: support-l1, position: 1"), "path_roles[1].position"},
		{"path_role position range", replace(validCatalog, "position: 2}", "position: 1000}"), "path_roles[1].position"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := catalog.ParseCatalog("catalog.yaml", []byte(tt.yaml))
			if err == nil {
				t.Fatalf("parsed %+v, want an error", c)
			}
			if !strings.Contains(err.Error(), "catalog.yaml") || !strings.Contains(err.Error(), tt.field) {
				t.Errorf("error %q must name the file and %q", err, tt.field)
			}
		})
	}
	if _, err := catalog.ParseCatalog("catalog.yaml", []byte(validCatalog)); err != nil {
		t.Errorf("valid catalog rejected: %v", err)
	}
}

func TestLoadCrossChecks(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(fstest.MapFS)
		want   string
	}{
		{"role not in catalog.yaml", func(m fstest.MapFS) {
			m["catalog.yaml"] = &fstest.MapFile{Data: []byte(replace(validCatalog,
				"  - {slug: support-l1, title: Support L1, kind: role, has_exam: true}\n", "",
				"  - {path: systems, role: support-l1, position: 2}\n", ""))}
		}, "support-l1/perms-01/ticket.yaml: role"},
		{"order repeated in a role", func(m fstest.MapFS) {
			m["support-l1/perms-02/ticket.yaml"] = &fstest.MapFile{Data: []byte(replace(validTicket, "perms-01", "perms-02"))}
			m["support-l1/perms-02/setup.sh"] = &fstest.MapFile{Data: []byte("s")}
			m["support-l1/perms-02/check.sh"] = &fstest.MapFile{Data: []byte("c")}
		}, "support-l1/perms-02/ticket.yaml: order"},
		{"id differs from directory", func(m fstest.MapFS) {
			m["support-l1/perms-01/ticket.yaml"] = &fstest.MapFile{Data: []byte(replace(validTicket, "perms-01", "perms-09"))}
		}, "support-l1/perms-01/ticket.yaml: id"},
		{"ticket without check.sh", func(m fstest.MapFS) { delete(m, "support-l1/perms-01/check.sh") }, "check.sh"},
		{"no catalog.yaml", func(m fstest.MapFS) { delete(m, "catalog.yaml") }, "catalog.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := fixtureTree()
			tt.mutate(m)
			c, err := catalog.Load(m)
			if err == nil {
				t.Fatalf("loaded %d tickets, want an error", len(c.Tickets))
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q lacks %q", err, tt.want)
			}
		})
	}
}

func TestLoadSortsTicketsAndReportsAllErrors(t *testing.T) {
	m := fixtureTree()
	c, err := catalog.Load(m)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tk := range c.Tickets {
		ids = append(ids, tk.ID)
	}
	if got := strings.Join(ids, " "); got != "foundations/find-01 support-l1/perms-01" {
		t.Errorf("tickets = %s", got)
	}

	m["support-l1/perms-01/ticket.yaml"] = &fstest.MapFile{Data: []byte(replace(validTicket, "order: 1", "order: 0"))}
	m["foundations/find-01/ticket.yaml"] = &fstest.MapFile{Data: []byte("nope: 1\n")}
	_, err = catalog.Load(m)
	if err == nil || !strings.Contains(err.Error(), "support-l1/perms-01/ticket.yaml") || !strings.Contains(err.Error(), "foundations/find-01/ticket.yaml") {
		t.Errorf("want both broken tickets reported, got %v", err)
	}
}

func TestFixtureCatalogIsValid(t *testing.T) {
	c, err := catalog.Load(os.DirFS("testdata/catalog"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tickets) != 4 || len(c.Paths) != 1 || c.Paths[0].Status != "available" {
		t.Errorf("fixture loaded as %+v", c)
	}
	var found, exam bool
	for _, r := range c.Roles {
		found = found || (r.Slug == "foundations" && r.Kind == "foundations" && !r.HasExam)
		exam = exam || (r.Slug == "support-l1" && r.HasExam)
	}
	if !found || !exam {
		t.Errorf("roles = %+v", c.Roles)
	}
}
