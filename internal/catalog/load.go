package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

var itemFileNames = []string{ticketFileName, WritingFileName, QuizFileName, StoryFileName, FeedbackFileName}

// Load reads catalog.yaml at the root of the tree and every item directory, <role>/<item>/ with
// exactly one of ticket.yaml, writing.yaml, quiz.yaml, story.yaml or feedback.yaml, plus the
// role-less practice/<slug>/ticket.yaml. It checks them against each other: a role must be in
// catalog.yaml, ids are unique, order is unique within a role across every kind, every awards_mark
// is a listed mark and every listed mark is awarded. It reports every problem it finds.
func Load(tree fs.FS) (*Catalog, error) {
	raw, err := fs.ReadFile(tree, catalogFileName)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", catalogFileName, err)
	}
	c, err := ParseCatalog(catalogFileName, raw)
	if err != nil {
		return nil, err
	}
	byDir := map[string][]string{}
	for _, name := range itemFileNames {
		files, err := fs.Glob(tree, "*/*/"+name)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			byDir[path.Dir(f)] = append(byDir[path.Dir(f)], name)
		}
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	l := &loader{tree: tree, c: c, roles: map[string]bool{}, marks: map[string]bool{},
		awarded: map[string]bool{}, orders: map[string]map[int]string{}}
	for _, r := range c.Roles {
		l.roles[r.Slug] = true
	}
	for _, m := range c.Marks {
		l.marks[m.Slug] = true
	}
	var errs []error
	for _, d := range dirs {
		names := byDir[d]
		switch {
		case len(names) > 1:
			errs = append(errs, &Error{File: d, Msg: fmt.Sprintf("has %s, want exactly one of %s", strings.Join(names, " and "), strings.Join(itemFileNames, ", "))})
		case IsPractice(d) && names[0] != ticketFileName:
			errs = append(errs, &Error{File: d, Msg: fmt.Sprintf("has %s, only %s is allowed under %s/", names[0], ticketFileName, PracticeDir)})
		default:
			if err := l.load(d, names[0]); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(c.Practice) > MaxOrder {
		errs = append(errs, &Error{File: PracticeDir, Msg: fmt.Sprintf("has %d tickets, at most %d", len(c.Practice), MaxOrder)})
	}
	if len(errs) == 0 {
		for i, m := range c.Marks {
			if !l.awarded[m.Slug] {
				errs = append(errs, &Error{File: catalogFileName, Field: fmt.Sprintf("marks[%d].slug", i),
					Msg: fmt.Sprintf("%q is not awarded by any writing or feedback item", m.Slug)})
			}
		}
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

type loader struct {
	tree    fs.FS
	c       *Catalog
	roles   map[string]bool
	marks   map[string]bool
	awarded map[string]bool
	orders  map[string]map[int]string // role -> order -> id, across every kind
}

// load reads the one file name of directory dir and adds it to the catalog when it holds up.
func (l *loader) load(dir, name string) error {
	file := path.Join(dir, name)
	is := &issues{file: file}
	var item *Item
	var awards string
	var commit func()
	if name == ticketFileName {
		t, err := LoadTicket(l.tree, dir)
		if err != nil {
			return err
		}
		if IsPractice(dir) {
			l.c.Practice = append(l.c.Practice, t)
			return nil
		}
		item, commit = &Item{ID: t.ID, Role: t.Role, Order: t.Order}, func() { l.c.Tickets = append(l.c.Tickets, t) }
	} else {
		raw, err := fs.ReadFile(l.tree, file)
		if err != nil {
			return err
		}
		switch name {
		case WritingFileName:
			w, err := ParseWriting(file, raw)
			if err != nil {
				return err
			}
			item, awards, commit = &w.Item, w.AwardsMark, func() { l.c.Writings = append(l.c.Writings, w) }
		case QuizFileName:
			q, err := ParseQuiz(file, raw)
			if err != nil {
				return err
			}
			item, commit = &q.Item, func() { l.c.Quizzes = append(l.c.Quizzes, q) }
		case StoryFileName:
			s, err := ParseStory(file, raw)
			if err != nil {
				return err
			}
			item, commit = &s.Item, func() { l.c.Stories = append(l.c.Stories, s) }
		case FeedbackFileName:
			f, err := ParseFeedback(file, raw)
			if err != nil {
				return err
			}
			item, awards, commit = &f.Item, f.AwardsMark, func() { l.c.Feedbacks = append(l.c.Feedbacks, f) }
		}
		if item.ID != dir {
			is.add("id", "is %q, want %q (the directory path)", item.ID, dir)
		}
	}
	if !l.roles[item.Role] {
		is.add("role", "%q is not a role in %s", item.Role, catalogFileName)
	}
	if l.orders[item.Role] == nil {
		l.orders[item.Role] = map[int]string{}
	}
	if other, dup := l.orders[item.Role][item.Order]; dup {
		is.add("order", "%d is already the order of %s in role %q", item.Order, other, item.Role)
	}
	l.orders[item.Role][item.Order] = item.ID
	if awards != "" {
		if l.marks[awards] {
			l.awarded[awards] = true
		} else {
			is.add("awards_mark", "%q is not a mark in %s", awards, catalogFileName)
		}
	}
	if err := is.err(); err != nil {
		return err
	}
	commit()
	return nil
}
