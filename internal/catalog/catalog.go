package catalog

import (
	"fmt"
	"slices"
	"strings"
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

// Belts is the fixed list of belts, in order. Belts are not catalog data.
var Belts = []string{"white", "yellow", "orange", "green", "blue", "brown", "black"}

const maxMarkSummary = 200

// Mark is a belt mark listed in catalog.yaml; its place in its belt is its order in the list.
type Mark struct {
	Slug    string `yaml:"slug"`
	Belt    string `yaml:"belt"`
	Title   string `yaml:"title"`
	Summary string `yaml:"summary"`
}

// Catalog is catalog.yaml plus every ticket and item read from the tree.
type Catalog struct {
	Paths     []Path     `yaml:"paths"`
	Roles     []Role     `yaml:"roles"`
	PathRoles []PathRole `yaml:"path_roles"`
	Marks     []Mark     `yaml:"marks"`

	// Filled by Load; ParseCatalog leaves them empty. Tickets are role tickets only, sorted by role
	// then order. Practice is sorted by id, the kind files by id within their kind.
	Tickets   []*Ticket   `yaml:"-"`
	Practice  []*Ticket   `yaml:"-"`
	Writings  []*Writing  `yaml:"-"`
	Quizzes   []*Quiz     `yaml:"-"`
	Stories   []*Story    `yaml:"-"`
	Feedbacks []*Feedback `yaml:"-"`
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
		if r.Slug == PracticeDir {
			is.add(f+".slug", "%q is reserved for practice tickets", r.Slug)
		}
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

	marks := map[string]bool{}
	for i := range c.Marks {
		m := &c.Marks[i]
		f := fmt.Sprintf("marks[%d]", i)
		checkSlug(is, f+".slug", m.Slug)
		if marks[m.Slug] {
			is.add(f+".slug", "%q appears twice", m.Slug)
		}
		marks[m.Slug] = true
		if !slices.Contains(Belts, m.Belt) {
			is.add(f+".belt", "%q must be one of %s", m.Belt, strings.Join(Belts, ", "))
		}
		if e := checkLine(m.Title, maxCatalogTitle); e != "" {
			is.add(f+".title", "%s", e)
		}
		if e := checkLine(m.Summary, maxMarkSummary); e != "" {
			is.add(f+".summary", "%s", e)
		}
	}
}
