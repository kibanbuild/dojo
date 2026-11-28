package catalog_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kibanyadev/dojo/internal/catalog"
)

const validWriting = `id: support-l1/write-01
title: Tell Elena what happened
role: support-l1
order: 2
est_minutes: 10
tags: [writing]
prompt: Elena asked why the folder broke.
rubric:
  - {id: found, text: What you found}
  - {id: next, text: What she does now}
awards_mark: written-english
`

const validQuiz = `id: support-l1/quiz-01
title: Users and logs
role: support-l1
order: 3
est_minutes: 5
questions:
  - id: who-last
    text: Who logged in last night?
    choices:
      - {text: "` + "`who`" + `", note: Lists current sessions}
      - {text: "` + "`last`" + `", note: Reads past logins from wtmp}
    answer: 1
    explain: last reads wtmp.
  - id: q2
    text: Second?
    choices: [{text: a, note: n}, {text: b, note: n}]
    answer: 0
  - id: q3
    text: Third?
    choices: [{text: a, note: n}, {text: b, note: n}]
    answer: 0
`

const validStory = `id: support-l1/story-01
title: Elena asks
role: support-l1
order: 4
est_minutes: 3
from: {name: Elena, role: Finance}
scene: The folder is broken again.
choices:
  - {id: fix, text: I will fix it now, verdict: good, reaction: She relaxes.}
  - {id: later, text: Later, verdict: poor, reaction: She waits.}
`

const validFeedback = `id: support-l1/feedback-01
title: Carmen's feedback
role: support-l1
order: 5
est_minutes: 10
from: {name: Carmen, role: Team lead}
scene: Your last reply was too short.
rubric:
  - {id: owns, text: Owns the miss}
  - {id: plan, text: Names the next step}
awards_mark: feedback-round
`

type parser func(string, []byte) error

var parsers = map[string]struct {
	file  string
	valid string
	parse parser
}{
	"writing":  {"writing.yaml", validWriting, func(f string, b []byte) error { _, err := catalog.ParseWriting(f, b); return err }},
	"quiz":     {"quiz.yaml", validQuiz, func(f string, b []byte) error { _, err := catalog.ParseQuiz(f, b); return err }},
	"story":    {"story.yaml", validStory, func(f string, b []byte) error { _, err := catalog.ParseStory(f, b); return err }},
	"feedback": {"feedback.yaml", validFeedback, func(f string, b []byte) error { _, err := catalog.ParseFeedback(f, b); return err }},
}

func TestKindFilesAcceptValid(t *testing.T) {
	for kind, p := range parsers {
		t.Run(kind, func(t *testing.T) {
			if err := p.parse(p.file, []byte(p.valid)); err != nil {
				t.Errorf("rejected a valid file: %v", err)
			}
		})
	}
}

func TestParseWritingFields(t *testing.T) {
	w, err := catalog.ParseWriting("w.yaml", []byte(validWriting))
	if err != nil {
		t.Fatal(err)
	}
	if w.ID != "support-l1/write-01" || w.Order != 2 || len(w.Rubric) != 2 || w.AwardsMark != "written-english" {
		t.Errorf("decoded %+v", w)
	}
}

type refuseCase struct {
	name  string
	kind  string
	yaml  string
	field string
}

func TestKindFilesRefuse(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	rub := func(n int) string {
		var b strings.Builder
		b.WriteString("rubric:\n")
		for i := range n {
			b.WriteString("  - {id: c" + string(rune('a'+i)) + ", text: t}\n")
		}
		return b.String()
	}
	two := "rubric:\n  - {id: found, text: What you found}\n  - {id: next, text: What she does now}\n"
	choice := func(text string) string { return "{text: " + text + ", note: n}" }
	tests := []refuseCase{
		// shared fields and strictness, on every kind
		{"writing unknown key", "writing", validWriting + "extra: 1\n", "extra"},
		{"quiz unknown key", "quiz", validQuiz + "extra: 1\n", "extra"},
		{"story unknown key", "story", validStory + "extra: 1\n", "extra"},
		{"feedback unknown key", "feedback", validFeedback + "extra: 1\n", "extra"},
		{"writing duplicate key", "writing", validWriting + "title: again\n", "title"},
		{"writing empty", "writing", "", "empty"},
		{"writing float", "writing", replace(validWriting, "order: 2", "order: 2.0"), "order"},
		{"quiz id upper case", "quiz", replace(validQuiz, "support-l1/quiz-01", "Support-l1/quiz-01"), "id"},
		{"story role differs", "story", replace(validStory, "role: support-l1", "role: other"), "role"},
		{"feedback order zero", "feedback", replace(validFeedback, "order: 5", "order: 0"), "order"},
		{"writing minutes 241", "writing", replace(validWriting, "est_minutes: 10", "est_minutes: 241"), "est_minutes"},
		{"writing repeated tag", "writing", replace(validWriting, "[writing]", "[a, a]"), "tags[1]"},
		{"writing title two lines", "writing", replace(validWriting, "title: Tell Elena what happened", `title: "a\nb"`), "title"},
		// reserved slugs
		{"writing reserved draft", "writing", replace(validWriting, "support-l1/write-01", "support-l1/draft"), "reserved"},
		{"quiz reserved submit", "quiz", replace(validQuiz, "support-l1/quiz-01", "support-l1/submit"), "reserved"},
		{"story reserved answers", "story", replace(validStory, "support-l1/story-01", "support-l1/answers"), "reserved"},
		{"feedback reserved choice", "feedback", replace(validFeedback, "support-l1/feedback-01", "support-l1/choice"), "reserved"},
		// writing
		{"prompt missing", "writing", replace(validWriting, "prompt: Elena asked why the folder broke.\n", ""), "prompt"},
		{"prompt blank", "writing", replace(validWriting, "Elena asked why the folder broke.", `"  "`), "prompt"},
		{"prompt too big", "writing", replace(validWriting, "Elena asked why the folder broke.", long(8193)), "prompt"},
		{"rubric missing", "writing", strings.Replace(validWriting, two, "", 1), "rubric"},
		{"rubric one", "writing", strings.Replace(validWriting, two, "rubric:\n  - {id: a, text: t}\n", 1), "rubric"},
		{"rubric seven", "writing", strings.Replace(validWriting, two, rub(7), 1), "rubric"},
		{"rubric id repeated", "writing", replace(validWriting, "id: next", "id: found"), "rubric[1].id"},
		{"rubric id upper", "writing", replace(validWriting, "id: found", "id: Found"), "rubric[0].id"},
		{"rubric id 33", "writing", replace(validWriting, "id: found", "id: "+long(33)), "rubric[0].id"},
		{"rubric text empty", "writing", replace(validWriting, "text: What you found", `text: ""`), "rubric[0].text"},
		{"rubric text 121", "writing", replace(validWriting, "What you found", long(121)), "rubric[0].text"},
		{"rubric text two lines", "writing", replace(validWriting, "text: What you found", `text: "a\nb"`), "rubric[0].text"},
		{"rubric unknown key", "writing", replace(validWriting, "{id: found, text: What you found}", "{id: found, text: t, note: x}"), "note"},
		{"mark not a slug", "writing", replace(validWriting, "written-english", "Written English"), "awards_mark"},
		// feedback
		{"feedback from missing", "feedback", replace(validFeedback, "from: {name: Carmen, role: Team lead}\n", ""), "from.name"},
		{"from name 41", "feedback", replace(validFeedback, "name: Carmen", "name: "+long(41)), "from.name"},
		{"from role empty", "feedback", replace(validFeedback, "role: Team lead", `role: ""`), "from.role"},
		{"scene missing", "feedback", replace(validFeedback, "scene: Your last reply was too short.\n", ""), "scene"},
		{"scene 4097", "feedback", replace(validFeedback, "Your last reply was too short.", long(4097)), "scene"},
		{"feedback rubric one", "feedback", strings.Replace(validFeedback, "  - {id: plan, text: Names the next step}\n", "", 1), "rubric"},
		{"feedback mark bad", "feedback", replace(validFeedback, "feedback-round", "feedback--round"), "awards_mark"},
		// story
		{"story scene missing", "story", replace(validStory, "scene: The folder is broken again.\n", ""), "scene"},
		{"story from name 41", "story", replace(validStory, "name: Elena", "name: "+long(41)), "from.name"},
		{"story one choice", "story", replace(validStory, "  - {id: later, text: Later, verdict: poor, reaction: She waits.}\n", ""), "choices"},
		{"story five choices", "story", validStory + strings.Repeat("  - {id: x, text: t, verdict: okay, reaction: r}\n", 3), "choices"},
		{"story choice id repeated", "story", replace(validStory, "id: later", "id: fix"), "choices[1].id"},
		{"story choice text 301", "story", replace(validStory, "I will fix it now", long(301)), "choices[0].text"},
		{"story verdict bad", "story", replace(validStory, "verdict: good", "verdict: great"), "choices[0].verdict"},
		{"story verdict missing", "story", replace(validStory, "verdict: good, ", ""), "choices[0].verdict"},
		{"story reaction missing", "story", replace(validStory, " reaction: She relaxes.", ` reaction: ""`), "choices[0].reaction"},
		{"story reaction 1001", "story", replace(validStory, "She relaxes.", long(1001)), "choices[0].reaction"},
		{"story trust field", "story", replace(validStory, "reaction: She relaxes.", "reaction: r, trust: 1"), "trust"},
		// quiz
		{"quiz two questions", "quiz", validQuiz[:strings.Index(validQuiz, "  - id: q3")], "questions"},
		{"quiz eleven questions", "quiz", validQuiz + strings.Repeat("  - {id: x, text: t, choices: [{text: a, note: n}, {text: b, note: n}], answer: 0}\n", 8), "questions"},
		{"quiz question id repeated", "quiz", replace(validQuiz, "id: q2", "id: q3"), "questions[2].id"},
		{"quiz question text missing", "quiz", replace(validQuiz, "text: Second?", `text: ""`), "questions[1].text"},
		{"quiz question text 1001", "quiz", replace(validQuiz, "Second?", long(1001)), "questions[1].text"},
		{"quiz one choice", "quiz", replace(validQuiz, "choices: [{text: a, note: n}, {text: b, note: n}]\n    answer: 0\n  - id: q3", "choices: [{text: a, note: n}]\n    answer: 0\n  - id: q3"), "questions[1].choices"},
		{"quiz six choices", "quiz", replace(validQuiz, "[{text: a, note: n}, {text: b, note: n}]", "["+strings.Join([]string{choice("a"), choice("b"), choice("c"), choice("d"), choice("e"), choice("f")}, ", ")+"]"), "questions[1].choices"},
		{"quiz choice repeated", "quiz", replace(validQuiz, "{text: b, note: n}", "{text: a, note: n}"), "questions[1].choices[1].text"},
		{"quiz choice text 201", "quiz", replace(validQuiz, "{text: a, note: n}", "{text: "+long(201)+", note: n}"), "questions[1].choices[0].text"},
		{"quiz choice text two lines", "quiz", replace(validQuiz, "{text: a, note: n}", `{text: "a\nb", note: n}`), "questions[1].choices[0].text"},
		{"quiz note missing", "quiz", replace(validQuiz, "{text: a, note: n}", "{text: a}"), "questions[1].choices[0].note"},
		{"quiz note 301", "quiz", replace(validQuiz, "note: n}", "note: "+long(301)+"}"), "note"},
		{"quiz answer missing", "quiz", strings.Replace(validQuiz, "    answer: 0\n", "", 1), "questions[1].answer"},
		{"quiz answer negative", "quiz", replace(validQuiz, "answer: 0", "answer: -1"), "questions[1].answer"},
		{"quiz answer past choices", "quiz", replace(validQuiz, "answer: 0", "answer: 2"), "questions[1].answer"},
		{"quiz answer a string", "quiz", replace(validQuiz, "answer: 0", "answer: first"), "answer"},
		{"quiz explain 1001", "quiz", replace(validQuiz, "last reads wtmp.", long(1001)), "questions[0].explain"},
		{"quiz explain blank", "quiz", replace(validQuiz, "explain: last reads wtmp.", `explain: " "`), "questions[0].explain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := parsers[tt.kind]
			err := p.parse("x/"+p.file, []byte(tt.yaml))
			if err == nil {
				t.Fatal("parsed, want an error")
			}
			if msg := err.Error(); !strings.Contains(msg, "x/"+p.file) || !strings.Contains(msg, tt.field) {
				t.Errorf("error %q must name the file and %q", msg, tt.field)
			}
		})
	}
}

func TestKindFilesBoundaries(t *testing.T) {
	long := func(n int) string { return strings.Repeat("é", n) }
	bytes := func(n int) string { return strings.Repeat("a", n) }
	rubric := func(n int) string {
		s := "rubric:\n"
		for i := range n {
			s += "  - {id: c" + string(rune('a'+i)) + ", text: t}\n"
		}
		return s
	}
	two := "rubric:\n  - {id: found, text: What you found}\n  - {id: next, text: What she does now}\n"
	tests := []struct{ name, kind, yaml string }{
		{"prompt 8192 bytes", "writing", replace(validWriting, "Elena asked why the folder broke.", bytes(8192))},
		{"six criteria", "writing", strings.Replace(validWriting, two, rubric(6), 1)},
		{"no mark", "writing", replace(validWriting, "awards_mark: written-english\n", "")},
		{"rubric id 32", "writing", replace(validWriting, "id: found", "id: "+bytes(32))},
		{"rubric text 120", "writing", replace(validWriting, "What you found", long(120))},
		{"scene 4096 bytes", "feedback", replace(validFeedback, "Your last reply was too short.", bytes(4096))},
		{"from 40 chars", "feedback", replace(validFeedback, "name: Carmen", "name: "+long(40))},
		{"feedback no mark", "feedback", replace(validFeedback, "awards_mark: feedback-round\n", "")},
		{"story reaction 1000", "story", replace(validStory, "She relaxes.", bytes(1000))},
		{"story reply 300", "story", replace(validStory, "I will fix it now", long(300))},
		{"story okay verdict", "story", replace(validStory, "verdict: good", "verdict: okay")},
		{"story four choices", "story", validStory +
			"  - {id: c, text: t, verdict: okay, reaction: r}\n  - {id: d, text: t, verdict: okay, reaction: r}\n"},
		{"quiz question 1000 bytes", "quiz", replace(validQuiz, "Second?", bytes(1000))},
		{"quiz choice 200 chars", "quiz", replace(validQuiz, "{text: a, note: n}", "{text: "+long(200)+", note: n}")},
		{"quiz note 300 chars", "quiz", replace(validQuiz, "note: n}", "note: "+long(300)+"}")},
		{"quiz explain absent", "quiz", replace(validQuiz, "    explain: last reads wtmp.\n", "")},
		{"quiz explain 1000", "quiz", replace(validQuiz, "last reads wtmp.", bytes(1000))},
		{"quiz answer last index", "quiz", replace(validQuiz, "answer: 0", "answer: 1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := parsers[tt.kind]
			if err := p.parse(p.file, []byte(tt.yaml)); err != nil {
				t.Errorf("rejected at the limit: %v", err)
			}
		})
	}
}

func TestQuizTenQuestionsAndFiveChoices(t *testing.T) {
	q := "  - {id: qN, text: t, choices: [{text: a, note: n}, {text: b, note: n}, {text: c, note: n}, {text: d, note: n}, {text: e, note: n}], answer: 4}\n"
	head := validQuiz[:strings.Index(validQuiz, "questions:")] + "questions:\n"
	var b strings.Builder
	b.WriteString(head)
	for i := range 10 {
		b.WriteString(strings.Replace(q, "qN", "q"+string(rune('a'+i)), 1))
	}
	if _, err := catalog.ParseQuiz("quiz.yaml", []byte(b.String())); err != nil {
		t.Errorf("rejected 10 questions of 5 choices: %v", err)
	}
}

func TestKindFilesReportEveryProblem(t *testing.T) {
	_, err := catalog.ParseStory("s.yaml", []byte(replace(validStory, "order: 4", "order: 0", "verdict: good", "verdict: nope")))
	if err == nil || !strings.Contains(err.Error(), "order") || !strings.Contains(err.Error(), "verdict") {
		t.Errorf("want both fields named, got %v", err)
	}
}

func TestKindPayloadJSON(t *testing.T) {
	w, _ := catalog.ParseWriting("w.yaml", []byte(validWriting))
	q, _ := catalog.ParseQuiz("q.yaml", []byte(validQuiz))
	s, _ := catalog.ParseStory("s.yaml", []byte(validStory))
	f, _ := catalog.ParseFeedback("f.yaml", []byte(validFeedback))
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"writing", w, `{"prompt":"Elena asked why the folder broke.","rubric":[{"id":"found","text":"What you found"},{"id":"next","text":"What she does now"}]}`},
		{"quiz", q, `{"questions":[` +
			`{"id":"who-last","text":"Who logged in last night?","choices":[{"text":"` + "`who`" + `","note":"Lists current sessions"},{"text":"` + "`last`" + `","note":"Reads past logins from wtmp"}],"answer":1,"explain":"last reads wtmp."},` +
			`{"id":"q2","text":"Second?","choices":[{"text":"a","note":"n"},{"text":"b","note":"n"}],"answer":0},` +
			`{"id":"q3","text":"Third?","choices":[{"text":"a","note":"n"},{"text":"b","note":"n"}],"answer":0}]}`},
		{"story", s, `{"from":{"name":"Elena","role":"Finance"},"scene":"The folder is broken again.","choices":[` +
			`{"id":"fix","text":"I will fix it now","verdict":"good","reaction":"She relaxes."},` +
			`{"id":"later","text":"Later","verdict":"poor","reaction":"She waits."}]}`},
		{"feedback", f, `{"from":{"name":"Carmen","role":"Team lead"},"scene":"Your last reply was too short.","rubric":[{"id":"owns","text":"Owns the miss"},{"id":"plan","text":"Names the next step"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.v)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("payload\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestReservedSlugsRefusedForTickets(t *testing.T) {
	for _, slug := range []string{"draft", "submit", "answers", "choice"} {
		id := "support-l1/" + slug
		if catalog.ValidTicketID(id) {
			t.Errorf("ValidTicketID(%q) = true", id)
		}
		_, err := catalog.ParseTicket("ticket.yaml", []byte(replace(validTicket, "support-l1/perms-01", id)))
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("ticket id %q: want a reserved-word error, got %v", id, err)
		}
	}
	for _, id := range []string{"support-l1/drafts", "support-l1/draft-01", "draft/perms-01"} {
		if !catalog.ValidTicketID(id) {
			t.Errorf("ValidTicketID(%q) = false, want true", id)
		}
	}
}
