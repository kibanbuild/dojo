package catalog_test

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kibanyadev/dojo/internal/catalog"
)

const validPractice = `id: practice/disk-full
title: Disk full at 3 a.m.
est_minutes: 20
tags: [storage]
difficulty: medium
brief: The disk is full.
hints: [one]
`

const marksYAML = `marks:
  - {slug: written-english, belt: white, title: Written English for tickets, summary: Write a reply}
  - {slug: feedback-round, belt: white, title: Feedback round with Carmen, summary: Take feedback}
`

func practiceTree() fstest.MapFS {
	m := fixtureTree()
	m["catalog.yaml"] = &fstest.MapFile{Data: []byte(validCatalog + marksYAML)}
	m["practice/disk-full/ticket.yaml"] = &fstest.MapFile{Data: []byte(validPractice)}
	m["practice/disk-full/setup.sh"] = &fstest.MapFile{Data: []byte("setup")}
	m["practice/disk-full/check.sh"] = &fstest.MapFile{Data: []byte("check")}
	m["support-l1/write-01/writing.yaml"] = &fstest.MapFile{Data: []byte(validWriting)}
	m["support-l1/feedback-01/feedback.yaml"] = &fstest.MapFile{Data: []byte(validFeedback)}
	m["support-l1/quiz-01/quiz.yaml"] = &fstest.MapFile{Data: []byte(validQuiz)}
	m["support-l1/story-01/story.yaml"] = &fstest.MapFile{Data: []byte(validStory)}
	return m
}

func TestLoadReadsPracticeMarksAndItems(t *testing.T) {
	c, err := catalog.Load(practiceTree())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tickets) != 2 {
		t.Errorf("Tickets = %d, practice must not be in it", len(c.Tickets))
	}
	if len(c.Practice) != 1 || c.Practice[0].ID != "practice/disk-full" || c.Practice[0].Difficulty != "medium" || c.Practice[0].Role != "" {
		t.Errorf("Practice = %+v", c.Practice)
	}
	if len(c.Writings) != 1 || len(c.Feedbacks) != 1 || len(c.Quizzes) != 1 || len(c.Stories) != 1 {
		t.Errorf("items = %d writing, %d feedback, %d quiz, %d story", len(c.Writings), len(c.Feedbacks), len(c.Quizzes), len(c.Stories))
	}
	if len(c.Marks) != 2 || c.Marks[0].Slug != "written-english" || c.Marks[0].Belt != "white" {
		t.Errorf("Marks = %+v", c.Marks)
	}
}

func TestLoadCapsPracticeTickets(t *testing.T) {
	for _, n := range []int{catalog.MaxOrder, catalog.MaxOrder + 1} {
		m := practiceTree()
		for i := 1; i < n; i++ {
			slug := fmt.Sprintf("p%04d", i)
			m["practice/"+slug+"/ticket.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(validPractice, "disk-full", slug, 1))}
			m["practice/"+slug+"/setup.sh"] = &fstest.MapFile{Data: []byte("setup")}
			m["practice/"+slug+"/check.sh"] = &fstest.MapFile{Data: []byte("check")}
		}
		_, err := catalog.Load(m)
		if (err == nil) != (n == catalog.MaxOrder) {
			t.Errorf("%d practice tickets: err = %v", n, err)
		}
	}
}

func TestBeltsAreTheFixedSeven(t *testing.T) {
	want := "white yellow orange green blue brown black"
	if got := strings.Join(catalog.Belts, " "); got != want {
		t.Errorf("Belts = %s", got)
	}
}

func TestParsePracticeTicketErrors(t *testing.T) {
	tests := []struct{ name, yaml, want string }{
		{"with a role", replace(validPractice, "title:", "role: support-l1\ntitle:"), "role"},
		{"with an order", replace(validPractice, "title:", "order: 1\ntitle:"), "order"},
		{"bad difficulty", replace(validPractice, "medium", "extreme"), "difficulty"},
		{"id not practice slug", replace(validPractice, "practice/disk-full", "practice/a/b"), "id"},
		{"reserved slug", replace(validPractice, "practice/disk-full", "practice/submit"), "reserved"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := catalog.ParseTicket("ticket.yaml", []byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v lacks %q", err, tt.want)
			}
		})
	}
	if _, err := catalog.ParseTicket("ticket.yaml", []byte(replace(validPractice, "difficulty: medium\n", ""))); err != nil {
		t.Errorf("difficulty is optional: %v", err)
	}
	if _, err := catalog.ParseTicket("ticket.yaml", []byte(replace(validTicket, "brief:", "difficulty: easy\nbrief:"))); err == nil || !strings.Contains(err.Error(), "difficulty") {
		t.Errorf("a role ticket may not carry difficulty, got %v", err)
	}
}

func TestParseCatalogMarksAndRoleSlugErrors(t *testing.T) {
	mark := func(s string) string { return validCatalog + "marks:\n  - " + s + "\n" }
	tests := []struct{ name, yaml, want string }{
		{"role named practice", replace(validCatalog, "slug: foundations,", "slug: practice,"), "practice"},
		{"bad mark slug", mark("{slug: Bad, belt: white, title: T, summary: S}"), "marks[0].slug"},
		{"unknown belt", mark("{slug: m, belt: purple, title: T, summary: S}"), "marks[0].belt"},
		{"long title", mark("{slug: m, belt: white, title: " + strings.Repeat("a", 81) + ", summary: S}"), "marks[0].title"},
		{"summary two lines", mark(`{slug: m, belt: white, title: T, summary: "a\nb"}`), "marks[0].summary"},
		{"long summary", mark("{slug: m, belt: white, title: T, summary: " + strings.Repeat("a", 201) + "}"), "marks[0].summary"},
		{"duplicate slug", mark("{slug: m, belt: white, title: T, summary: S}\n  - {slug: m, belt: blue, title: T, summary: S}"), "marks[1].slug"},
		{"unknown key", mark("{slug: m, belt: white, title: T, summary: S, order: 1}"), "order"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := catalog.ParseCatalog("catalog.yaml", []byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v lacks %q", err, tt.want)
			}
		})
	}
}

func TestLoadCrossFileErrors(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	tests := []struct {
		name   string
		mutate func(fstest.MapFS)
		want   string
	}{
		{"two kind files in one directory", func(m fstest.MapFS) {
			m["support-l1/write-01/quiz.yaml"] = file(validQuiz)
		}, "support-l1/write-01"},
		{"kind file next to a ticket", func(m fstest.MapFS) {
			m["support-l1/perms-01/writing.yaml"] = file(validWriting)
		}, "support-l1/perms-01"},
		{"kind file under practice", func(m fstest.MapFS) {
			m["practice/disk-full/writing.yaml"] = file(validWriting)
		}, "practice/disk-full"},
		{"item id differs from directory", func(m fstest.MapFS) {
			m["support-l1/write-01/writing.yaml"] = file(replace(validWriting, "write-01", "write-09"))
		}, "support-l1/write-01/writing.yaml: id"},
		{"item role not in catalog", func(m fstest.MapFS) {
			m["ghost/write-01/writing.yaml"] = file(replace(validWriting, "support-l1", "ghost"))
		}, "ghost/write-01/writing.yaml: role"},
		{"item order clashes with a ticket", func(m fstest.MapFS) {
			m["support-l1/write-01/writing.yaml"] = file(replace(validWriting, "order: 2", "order: 1"))
		}, "support-l1/write-01/writing.yaml: order"},
		{"item order clashes with another kind", func(m fstest.MapFS) {
			m["support-l1/feedback-01/feedback.yaml"] = file(replace(validFeedback, "order: 5", "order: 2"))
		}, "order"},
		{"awards_mark not listed", func(m fstest.MapFS) {
			m["support-l1/write-01/writing.yaml"] = file(replace(validWriting, "written-english", "ghost-mark"))
		}, "ghost-mark"},
		{"mark nobody awards", func(m fstest.MapFS) {
			m["catalog.yaml"] = file(validCatalog + marksYAML + "  - {slug: lonely, belt: white, title: T, summary: S}\n")
		}, "lonely"},
		{"mark loses its only awarding item", func(m fstest.MapFS) {
			delete(m, "support-l1/feedback-01/feedback.yaml")
		}, "feedback-round"},
		{"practice with setup missing", func(m fstest.MapFS) { delete(m, "practice/disk-full/check.sh") }, "check.sh"},
		{"item in a role named practice", func(m fstest.MapFS) {
			m["practice/x/quiz.yaml"] = file(replace(validQuiz, "support-l1/quiz-01", "practice/x"))
		}, "practice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := practiceTree()
			tt.mutate(m)
			if _, err := catalog.Load(m); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v lacks %q", err, tt.want)
			}
		})
	}
}

func TestPracticeBundleKeepsItsPath(t *testing.T) {
	b, err := catalog.BuildBundle(practiceTree(), "practice/disk-full")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.Key(), "bundles/practice/disk-full/") {
		t.Errorf("key = %s", b.Key())
	}
}
