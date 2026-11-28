package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStandinMeetsCriteriaByWholeWord(t *testing.T) {
	tests := []struct {
		name, body string
		wantFound  bool
	}{
		{"plain", "I found it", true},
		{"case insensitive", "I FOUND it", true},
		{"inside a longer word", "unfounded claims", false},
		{"prefix of a word", "founder", false},
		{"punctuation around", "(found).", true},
		{"absent", "nothing here", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rev, usage, err := Review(context.Background(), Standin{}, Request{Prompt: "p", Rubric: testRubric, Body: tt.body})
			if err != nil {
				t.Fatal(err)
			}
			if rev.Criteria[0].Met != tt.wantFound {
				t.Errorf("found met = %v, want %v", rev.Criteria[0].Met, tt.wantFound)
			}
			if usage.Model != "standin" || usage.TokensOut != 50 || usage.TokensIn != len(tt.body)/4 {
				t.Errorf("usage = %+v", usage)
			}
		})
	}
}

func TestStandinReviewShape(t *testing.T) {
	body := "Hi Elena,\n  I  found the cause:\tthe group.\nThen next time ask."
	rev, _, err := Review(context.Background(), Standin{}, Request{Prompt: "p", Rubric: testRubric, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	c := rev.Criteria
	if !c[0].Met || c[0].Evidence != "I found the cause: the group." || c[0].Note != "" {
		t.Errorf("met criterion = %+v", c[0])
	}
	if !c[1].Met || c[1].Evidence != "Then next time ask." {
		t.Errorf("second criterion = %+v", c[1])
	}
	if rev.NextStep != "" {
		t.Errorf("next step = %q, want empty when all met", rev.NextStep)
	}

	rev, _, err = Review(context.Background(), Standin{}, Request{Prompt: "p", Rubric: testRubric, Body: "found"})
	if err != nil {
		t.Fatal(err)
	}
	if rev.Criteria[1].Note != "Not found: What she does now" || rev.NextStep != "Add: What she does now" {
		t.Errorf("unmet = %+v / %q", rev.Criteria[1], rev.NextStep)
	}
}

func TestStandinEvidenceCut(t *testing.T) {
	words := strings.Repeat("word ", 80) + "found tail"
	rev, _, err := Review(context.Background(), Standin{}, Request{Prompt: "p", Rubric: testRubric, Body: "found " + words})
	if err != nil {
		t.Fatal(err)
	}
	ev := rev.Criteria[0].Evidence
	if n := len([]rune(ev)); n > 300 || strings.HasSuffix(ev, " ") || !strings.HasSuffix(ev, "word") {
		t.Errorf("evidence cut = %d runes, %q", n, ev[max(0, len(ev)-12):])
	}

	long := "found" + strings.Repeat("x", 400)
	rev, _, err = Review(context.Background(), Standin{}, Request{Prompt: "p", Rubric: []Criterion{{ID: "found", Text: "a"}, {ID: "zz", Text: "b"}}, Body: "found " + long})
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(rev.Criteria[0].Evidence)); got != 6+300-6 && got > 300 {
		t.Errorf("first word longer than the cap must cut at 300, got %d", got)
	}
}

func TestStandinFailureSwitches(t *testing.T) {
	_, _, err := Review(context.Background(), Standin{}, Request{Prompt: "p", Rubric: testRubric, Body: "found [[standin:error]]"})
	if err == nil || errors.Is(err, ErrInvalidReply) {
		t.Errorf("error switch: err = %v, want an adapter error", err)
	}
	_, usage, err := Review(context.Background(), Standin{}, Request{Prompt: "p", Rubric: testRubric, Body: "found [[standin:invalid]]"})
	if !errors.Is(err, ErrInvalidReply) {
		t.Errorf("invalid switch: err = %v, want ErrInvalidReply", err)
	}
	if usage.Model != "standin" {
		t.Errorf("usage is kept on an invalid reply: %+v", usage)
	}
}
