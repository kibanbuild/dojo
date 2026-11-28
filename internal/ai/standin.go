package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Standin is the deterministic adapter used by compose and tests (section 3.10). A body with
// [[standin:error]] makes it fail; one with [[standin:invalid]] makes it answer invalid JSON.
type Standin struct{}

type standinCriterion struct {
	ID       string `json:"id"`
	Met      bool   `json:"met"`
	Evidence string `json:"evidence"`
	Note     string `json:"note"`
}

// Review implements Adapter.
func (Standin) Review(_ context.Context, req Request) (RawReply, error) {
	usage := RawReply{Model: "standin", TokensIn: len(req.Body) / 4, TokensOut: 50}
	if strings.Contains(req.Body, "[[standin:error]]") {
		return RawReply{}, errors.New("standin: forced error")
	}
	if strings.Contains(req.Body, "[[standin:invalid]]") {
		usage.Text = "not json"
		return usage, nil
	}
	out := struct {
		Criteria []standinCriterion `json:"criteria"`
		NextStep string             `json:"next_step"`
	}{Criteria: []standinCriterion{}}
	for _, c := range req.Rubric {
		sc := standinCriterion{ID: c.ID}
		if line, ok := firstLineWith(req.Body, c.ID); ok {
			sc.Met, sc.Evidence = true, cutAtSpace(normalize(line), maxEvidenceChars)
		} else {
			sc.Note = "Not found: " + c.Text
			if out.NextStep == "" {
				out.NextStep = "Add: " + c.Text
			}
		}
		out.Criteria = append(out.Criteria, sc)
	}
	b, err := json.Marshal(out)
	usage.Text = string(b)
	return usage, err
}

func firstLineWith(body, word string) (string, bool) {
	for _, line := range strings.Split(body, "\n") {
		if hasWord(strings.ToLower(line), strings.ToLower(word)) {
			return line, true
		}
	}
	return "", false
}

// hasWord reports whether w occurs in s with no letter or digit on either side.
func hasWord(s, w string) bool {
	for from := 0; ; {
		i := strings.Index(s[from:], w)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(w)
		prev, _ := utf8.DecodeLastRuneInString(s[:i])
		next, _ := utf8.DecodeRuneInString(s[end:])
		okBefore := i == 0 || !isWordRune(prev)
		okAfter := end == len(s) || !isWordRune(next)
		if okBefore && okAfter {
			return true
		}
		from = i + 1
	}
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// cutAtSpace keeps s whole when it fits in max characters, else cuts at the last space inside max,
// or at max when the first word is longer.
func cutAtSpace(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if r[max] == ' ' {
		return string(r[:max])
	}
	if i := strings.LastIndex(string(r[:max]), " "); i > 0 {
		return string(r[:max])[:i]
	}
	return string(r[:max])
}
