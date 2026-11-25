package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
)

// Path statuses and role kinds allowed in catalog.yaml.
const (
	StatusAvailable = "available"
	StatusPlanned   = "planned"
	KindFoundations = "foundations"
	KindRole        = "role"
	maxPosition     = 999
	maxSlugLen      = 40
	maxCatalogTitle = 80
	maxSummary      = 120
)

// Path is a learning path: an ordered list of roles (see PathRole).
type Path struct {
	Slug     string `yaml:"slug"`
	Title    string `yaml:"title"`
	Position int    `yaml:"position"`
	Status   string `yaml:"status"`
}

// Role is a set of tickets, optionally with a role exam. Several paths may share one.
type Role struct {
	Slug     string `yaml:"slug"`
	Title    string `yaml:"title"`
	Summary  string `yaml:"summary"`
	Kind     string `yaml:"kind"`
	HasExam  bool   `yaml:"has_exam"`
	Advanced bool   `yaml:"advanced"`
}

// PathRole places a role in a path.
type PathRole struct {
	Path     string `yaml:"path"`
	Role     string `yaml:"role"`
	Position int    `yaml:"position"`
}

// Catalog is catalog.yaml plus every ticket read from the tree.
type Catalog struct {
	Paths     []Path     `yaml:"paths"`
	Roles     []Role     `yaml:"roles"`
	PathRoles []PathRole `yaml:"path_roles"`

	// Tickets is filled by Load, sorted by role then order. ParseCatalog leaves it empty.
	Tickets []*Ticket `yaml:"-"`
}

// ParseCatalog decodes and validates the bytes of a catalog.yaml; file names it in errors.
func ParseCatalog(file string, raw []byte) (*Catalog, error) {
	var c Catalog
	if err := decodeStrict(file, raw, &c); err != nil {
		return nil, err
	}
	is := &issues{file: file}
	c.validate(is)
	if err := is.err(); err != nil {
		return nil, err
	}
	return &c, nil
}

func checkSlug(is *issues, field, slug string) {
	switch {
	case !slugRE.MatchString(slug):
		is.add(field, "%q must be lower-case letters, digits and single hyphens", slug)
	case len(slug) > maxSlugLen:
		is.add(field, "%q is longer than %d characters", slug, maxSlugLen)
	}
}

func (c *Catalog) validate(is *issues) {
	paths := map[string]*Path{}
	positions := map[int]string{}
	if len(c.Paths) == 0 {
		is.add("paths", "needs at least one path")
	}
	for i := range c.Paths {
		p := &c.Paths[i]
		f := fmt.Sprintf("paths[%d]", i)
		checkSlug(is, f+".slug", p.Slug)
		if paths[p.Slug] != nil {
			is.add(f+".slug", "%q appears twice", p.Slug)
		}
		paths[p.Slug] = p
		if e := checkLine(p.Title, maxCatalogTitle); e != "" {
			is.add(f+".title", "%s", e)
		}
		if p.Position < 1 || p.Position > maxPosition {
			is.add(f+".position", "must be 1 to %d, got %d", maxPosition, p.Position)
		} else if other, dup := positions[p.Position]; dup {
			is.add(f+".position", "%d is already the position of path %q", p.Position, other)
		}
		positions[p.Position] = p.Slug
		if p.Status != StatusAvailable && p.Status != StatusPlanned {
			is.add(f+".status", "%q must be %q or %q", p.Status, StatusAvailable, StatusPlanned)
		}
	}

	roles := map[string]bool{}
	if len(c.Roles) == 0 {
		is.add("roles", "needs at least one role")
	}
	for i := range c.Roles {
		r := &c.Roles[i]
		f := fmt.Sprintf("roles[%d]", i)
		checkSlug(is, f+".slug", r.Slug)
		if roles[r.Slug] {
			is.add(f+".slug", "%q appears twice", r.Slug)
		}
		roles[r.Slug] = true
		if e := checkLine(r.Title, maxCatalogTitle); e != "" {
			is.add(f+".title", "%s", e)
		}
		if e := checkLine(r.Summary, maxSummary); e != "" {
			is.add(f+".summary", "role %q: %s", r.Slug, e)
		}
		if r.Kind != KindFoundations && r.Kind != KindRole {
			is.add(f+".kind", "%q must be %q or %q", r.Kind, KindFoundations, KindRole)
		}
	}

	pairs := map[[2]string]bool{}
	slots := map[string]map[int]string{}
	for i := range c.PathRoles {
		pr := &c.PathRoles[i]
		f := fmt.Sprintf("path_roles[%d]", i)
		if paths[pr.Path] == nil {
			is.add(f+".path", "%q is not in paths", pr.Path)
		}
		if !roles[pr.Role] {
			is.add(f+".role", "%q is not in roles", pr.Role)
		}
		key := [2]string{pr.Path, pr.Role}
		if pairs[key] {
			is.add(f, "role %q appears twice in path %q", pr.Role, pr.Path)
		}
		pairs[key] = true
		if pr.Position < 1 || pr.Position > maxPosition {
			is.add(f+".position", "must be 1 to %d, got %d", maxPosition, pr.Position)
			continue
		}
		if slots[pr.Path] == nil {
			slots[pr.Path] = map[int]string{}
		}
		if other, dup := slots[pr.Path][pr.Position]; dup {
			is.add(f+".position", "%d is already the position of role %q in path %q", pr.Position, other, pr.Path)
		}
		slots[pr.Path][pr.Position] = pr.Role
	}
}

// Load reads catalog.yaml at the root of the tree and every <role>/<ticket>/ticket.yaml, and
// checks them against each other: a ticket's role must be in catalog.yaml, ids are unique and
// order is unique within a role. It reports every problem it finds, not only the first.
func Load(tree fs.FS) (*Catalog, error) {
	raw, err := fs.ReadFile(tree, catalogFileName)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", catalogFileName, err)
	}
	c, err := ParseCatalog(catalogFileName, raw)
	if err != nil {
		return nil, err
	}
	files, err := fs.Glob(tree, "*/*/"+ticketFileName)
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	roles := map[string]bool{}
	for _, r := range c.Roles {
		roles[r.Slug] = true
	}
	var errs []error
	ids := map[string]bool{}
	orders := map[string]map[int]string{}
	for _, f := range files {
		dir := path.Dir(f)
		t, err := LoadTicket(tree, dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		is := &issues{file: f}
		if !roles[t.Role] {
			is.add("role", "%q is not a role in %s", t.Role, catalogFileName)
		}
		if ids[t.ID] {
			is.add("id", "%q appears twice", t.ID)
		}
		ids[t.ID] = true
		if orders[t.Role] == nil {
			orders[t.Role] = map[int]string{}
		}
		if other, dup := orders[t.Role][t.Order]; dup {
			is.add("order", "%d is already the order of %s in role %q", t.Order, other, t.Role)
		}
		orders[t.Role][t.Order] = t.ID
		if is.err() != nil {
			errs = append(errs, is.err())
			continue
		}
		c.Tickets = append(c.Tickets, t)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	sort.Slice(c.Tickets, func(i, j int) bool {
		a, b := c.Tickets[i], c.Tickets[j]
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.Order < b.Order
	})
	return c, nil
}
