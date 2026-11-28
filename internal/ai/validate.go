package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	maxEvidenceChars = 300
	maxNoteChars     = 200
	maxNextStepChars = 300
)

// normalize collapses every run of Unicode whitespace to one space and trims the ends; nothing else.
func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidReply, fmt.Sprintf(format, args...))
}

// exactObject reads a JSON object whose keys are exactly keys: matched case-sensitively, none
// repeated, none missing, none extra. The values stay raw.
func exactObject(raw []byte, keys ...string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, invalid("not an object")
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, invalid("%v", err)
		}
		k, _ := tok.(string)
		if _, dup := out[k]; dup {
			return nil, invalid("duplicate key %q", k)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, invalid("%v", err)
		}
		out[k] = v
	}
	if _, err := dec.Token(); err != nil {
		return nil, invalid("%v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, invalid("data after the object")
	}
	if len(out) != len(keys) {
		return nil, invalid("expected exactly the keys %v", keys)
	}
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			return nil, invalid("missing key %q", k)
		}
	}
	return out, nil
}

func str(raw json.RawMessage, field string, min, max int) (string, error) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", invalid("%s is not a string", field)
	}
	// Postgres jsonb refuses U+0000.
	if strings.ContainsRune(s, 0) {
		return "", invalid("%s contains NUL", field)
	}
	if n := utf8.RuneCountInString(s); n < min || n > max {
		return "", invalid("%s has %d characters, want %d to %d", field, n, min, max)
	}
	return s, nil
}

// validateReply checks the model's text against the schema of section 3.10 and returns the review in
// rubric order, with each criterion's text copied from the rubric.
func validateReply(text string, rubric []Criterion, body string) (Result, error) {
	if len(text) > maxReplyBytes {
		return Result{}, invalid("reply over %d bytes", maxReplyBytes)
	}
	top, err := exactObject([]byte(text), "criteria", "next_step")
	if err != nil {
		return Result{}, err
	}
	if top["criteria"][0] != '[' {
		return Result{}, invalid("criteria is not an array")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(top["criteria"], &items); err != nil || len(items) != len(rubric) {
		return Result{}, invalid("want exactly %d criteria", len(rubric))
	}
	byID := map[string]CriterionResult{}
	nbody := normalize(body)
	allMet := true
	for _, item := range items {
		obj, err := exactObject(item, "id", "met", "evidence", "note")
		if err != nil {
			return Result{}, err
		}
		id, err := str(obj["id"], "id", 1, 1<<10)
		if err != nil {
			return Result{}, err
		}
		if _, dup := byID[id]; dup {
			return Result{}, invalid("criterion %q repeated", id)
		}
		metRaw := string(obj["met"])
		if metRaw != "true" && metRaw != "false" {
			return Result{}, invalid("met of %q is not a boolean", id)
		}
		met := metRaw == "true"
		minEv := 0
		if met {
			minEv = 1
		}
		ev, err := str(obj["evidence"], "evidence", minEv, maxEvidenceChars)
		if err != nil {
			return Result{}, err
		}
		switch {
		case met && !strings.Contains(nbody, normalize(ev)):
			return Result{}, invalid("evidence of %q is not in the text", id)
		case met && normalize(ev) == "":
			return Result{}, invalid("evidence of %q is blank", id)
		case !met && ev != "":
			return Result{}, invalid("evidence of %q must be empty when not met", id)
		}
		note, err := str(obj["note"], "note", 0, maxNoteChars)
		if err != nil {
			return Result{}, err
		}
		allMet = allMet && met
		byID[id] = CriterionResult{ID: id, Met: met, Evidence: ev, Note: note}
	}
	next, err := str(top["next_step"], "next_step", 0, maxNextStepChars)
	if err != nil {
		return Result{}, err
	}
	if next == "" && !allMet {
		return Result{}, invalid("next_step is empty but a criterion is not met")
	}
	rev := Result{NextStep: next}
	for _, c := range rubric {
		r, ok := byID[c.ID]
		if !ok {
			return Result{}, invalid("rubric id %q missing", c.ID)
		}
		r.Text = c.Text
		rev.Criteria = append(rev.Criteria, r)
	}
	return rev, nil
}
