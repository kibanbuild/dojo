package ai

import (
	"errors"
	"strings"
	"testing"
)

var testRubric = []Criterion{
	{ID: "found", Text: "What you found"},
	{ID: "next", Text: "What she does now"},
}

const testBody = "Hi Elena,\nThe  team group now owns /srv/shared.\n\tPlease retry."

func TestNormalize(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"only space", " \t\n ", ""},
		{"runs and newlines", "a  b\n\tc", "a b c"},
		{"ends trimmed", "  a b  ", "a b"},
		{"case kept", "Hello  WORLD", "Hello WORLD"},
		{"unicode space", "a b c", "a b c"},
		{"no unicode normalization", "é", "é"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalize(tt.in); got != tt.want {
				t.Errorf("normalize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func reply(found, next string) string {
	return `{"criteria":[` + found + `,` + next + `],"next_step":"Add one line."}`
}

const (
	metFound = `{"id":"found","met":true,"evidence":"The team group now owns /srv/shared.","note":""}`
	metNext  = `{"id":"next","met":true,"evidence":"Please retry.","note":""}`
	noNext   = `{"id":"next","met":false,"evidence":"","note":"Nothing tells her what to do."}`
)

func TestValidateReplyAccepts(t *testing.T) {
	r, err := validateReply(reply(metFound, noNext), testRubric, testBody)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Criteria) != 2 || r.Criteria[0].Text != "What you found" || !r.Criteria[0].Met || r.Criteria[1].Met {
		t.Errorf("unexpected review: %+v", r)
	}
	if r.NextStep != "Add one line." {
		t.Errorf("next step = %q", r.NextStep)
	}

	all := `{"criteria":[` + metFound + `,` + metNext + `],"next_step":""}`
	if _, err := validateReply(all, testRubric, testBody); err != nil {
		t.Errorf("all met with empty next_step: %v", err)
	}

	swapped := `{"criteria":[` + noNext + `,` + metFound + `],"next_step":"x"}`
	r, err = validateReply(swapped, testRubric, testBody)
	if err != nil || r.Criteria[0].ID != "found" {
		t.Errorf("order of reply must not matter, rubric order wins: %v %+v", err, r)
	}

	spaced := strings.Replace(metFound, "The team group", "The   team\\ngroup", 1)
	if _, err := validateReply(reply(spaced, noNext), testRubric, testBody); err != nil {
		t.Errorf("evidence is compared after normalize: %v", err)
	}
}

func TestValidateReplyRejects(t *testing.T) {
	long := strings.Repeat("x", 301)
	tests := []struct{ name, raw string }{
		{"not json", `nope`},
		{"empty", ``},
		{"array", `[]`},
		{"null", `null`},
		{"trailing data", reply(metFound, noNext) + ` {}`},
		{"unknown top field", `{"criteria":[` + metFound + `,` + noNext + `],"next_step":"x","extra":1}`},
		{"missing next_step", `{"criteria":[` + metFound + `,` + noNext + `]}`},
		{"missing criteria", `{"next_step":"x"}`},
		{"criteria null", `{"criteria":null,"next_step":"x"}`},
		{"duplicate top key", `{"criteria":[` + metFound + `,` + noNext + `],"next_step":"x","next_step":"y"}`},
		{"wrong case key", `{"Criteria":[` + metFound + `,` + noNext + `],"next_step":"x"}`},
		{"next_step not string", `{"criteria":[` + metFound + `,` + noNext + `],"next_step":null}`},
		{"unknown criterion field", reply(`{"id":"found","met":true,"evidence":"Hi Elena,","note":"","text":"x"}`, noNext)},
		{"missing criterion field", reply(`{"id":"found","met":true,"evidence":"Hi Elena,"}`, noNext)},
		{"duplicate criterion key", reply(`{"id":"found","id":"found","met":true,"evidence":"Hi Elena,","note":""}`, noNext)},
		{"criterion not object", reply(`"found"`, noNext)},
		{"missing rubric id", `{"criteria":[` + metFound + `],"next_step":"x"}`},
		{"duplicate id", reply(metFound, metFound)},
		{"unknown id", reply(metFound, `{"id":"other","met":false,"evidence":"","note":""}`)},
		{"extra criterion", `{"criteria":[` + metFound + `,` + noNext + `,` + metFound + `],"next_step":"x"}`},
		{"met string", reply(`{"id":"found","met":"true","evidence":"Hi Elena,","note":""}`, noNext)},
		{"met null", reply(`{"id":"found","met":null,"evidence":"","note":""}`, noNext)},
		{"NUL in note", reply(metFound, `{"id":"next","met":false,"evidence":"","note":"a\u0000b"}`)},
		{"met number", reply(`{"id":"found","met":1,"evidence":"Hi Elena,","note":""}`, noNext)},
		{"id not string", reply(`{"id":1,"met":false,"evidence":"","note":""}`, noNext)},
		{"met without evidence", reply(`{"id":"found","met":true,"evidence":"","note":""}`, noNext)},
		{"met with blank evidence", reply(`{"id":"found","met":true,"evidence":"  \n","note":""}`, noNext)},
		{"evidence not in body", reply(`{"id":"found","met":true,"evidence":"The team group owns it","note":""}`, noNext)},
		{"evidence case differs", reply(`{"id":"found","met":true,"evidence":"hi elena,","note":""}`, noNext)},
		{"evidence over 300", reply(`{"id":"found","met":true,"evidence":"`+long+`","note":""}`, noNext)},
		{"unmet with evidence", reply(metFound, `{"id":"next","met":false,"evidence":"Please retry.","note":"x"}`)},
		{"note over 200", reply(metFound, `{"id":"next","met":false,"evidence":"","note":"`+strings.Repeat("n", 201)+`"}`)},
		{"empty next_step with unmet", `{"criteria":[` + metFound + `,` + noNext + `],"next_step":""}`},
		{"next_step over 300", `{"criteria":[` + metFound + `,` + noNext + `],"next_step":"` + long + `"}`},
		{"reply over size cap", reply(metFound, noNext) + strings.Repeat(" ", maxReplyBytes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateReply(tt.raw, testRubric, testBody)
			if !errors.Is(err, ErrInvalidReply) {
				t.Errorf("err = %v, want ErrInvalidReply", err)
			}
		})
	}
}

func TestValidateReplyCountsCharactersNotBytes(t *testing.T) {
	body := strings.Repeat("é", 300)
	ev := `{"id":"found","met":true,"evidence":"` + body + `","note":""}`
	if _, err := validateReply(reply(ev, noNext), testRubric, body); err != nil {
		t.Errorf("300 characters of 2 bytes each must pass: %v", err)
	}
	body += "é"
	ev = `{"id":"found","met":true,"evidence":"` + body + `","note":""}`
	if _, err := validateReply(reply(ev, noNext), testRubric, body); !errors.Is(err, ErrInvalidReply) {
		t.Errorf("301 characters must fail: %v", err)
	}
}
