// Package catalog reads the Dojo catalog: catalog.yaml, every ticket.yaml, and the reproducible
// bundle built from a ticket directory (docs/technical-design.md, section 3.8). The deploy job and
// the CLI validate with this same code.
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Limits from section 3.8.
const (
	MaxTitleChars   = 80
	MaxBriefBytes   = 8192
	MaxHintChars    = 1000
	MaxHints        = 3
	MaxTags         = 8
	MaxEstMinutes   = 240
	MaxOrder        = 999
	minIDLen        = 3
	maxIDLen        = 64
	ticketFileName  = "ticket.yaml"
	catalogFileName = "catalog.yaml"
)

var (
	slugRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	idRE   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*/[a-z0-9]+(-[a-z0-9]+)*$`)
	tagRE  = regexp.MustCompile(`^[a-z0-9-]{1,24}$`)
)

// ValidTicketID reports whether id is exactly <role-slug>/<ticket-slug>.
func ValidTicketID(id string) bool {
	return len(id) <= maxIDLen && idRE.MatchString(id)
}

// ErrNotFound means the catalog has no ticket with that id.
var ErrNotFound = errors.New("ticket not found")

// Error is a validation failure that names the file and, when known, the field.
type Error struct {
	File  string
	Field string
	Msg   string
}

func (e *Error) Error() string {
	if e.Field == "" {
		return e.File + ": " + e.Msg
	}
	return e.File + ": " + e.Field + ": " + e.Msg
}

// issues collects every problem of one file, so an author fixes them in one pass.
type issues struct {
	file string
	errs []error
}

func (is *issues) add(field, format string, args ...any) {
	is.errs = append(is.errs, &Error{File: is.file, Field: field, Msg: fmt.Sprintf(format, args...)})
}

func (is *issues) err() error { return errors.Join(is.errs...) }

// Ticket is one exercise: what to tell the learner and how to break and verify the lab.
type Ticket struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	Role       string   `yaml:"role"`
	Order      int      `yaml:"order"`
	EstMinutes int      `yaml:"est_minutes"`
	Tags       []string `yaml:"tags"`
	Brief      string   `yaml:"brief"`
	Hints      []string `yaml:"hints"`

	// Setup and Check are read by LoadTicket (CRLF turned into LF); ParseTicket leaves them empty.
	Setup []byte `yaml:"-"`
	Check []byte `yaml:"-"`
}

// decodeStrict decodes exactly one YAML document into v. An unknown key, a duplicate key, an empty
// file and a second document are all errors, so a typo like "hint:" can never drop the hints.
func decodeStrict(file string, raw []byte, v any) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err == nil {
		var errs []error
		checkNode(file, &doc, "", &errs)
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
	} // a syntax error is reported by the strict decode below
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return &Error{File: file, Msg: "the file is empty"}
		}
		return typeErrors(file, raw, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return &Error{File: file, Msg: "more than one YAML document"}
	}
	return nil
}

// checkNode refuses YAML merge keys (<<), which pull in fields from elsewhere in the file, and
// any float: no field of either schema is a float, so "order: 1.0" or "1e2" is a mistake that yaml
// would otherwise accept as an integer.
func checkNode(file string, n *yaml.Node, field string, errs *[]error) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for i, c := range n.Content {
			f := field
			if n.Kind == yaml.SequenceNode {
				f = fmt.Sprintf("%s[%d]", field, i)
			}
			checkNode(file, c, f, errs)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Tag == "!!merge" {
				*errs = append(*errs, &Error{File: file, Field: "<<",
					Msg: fmt.Sprintf("line %d: merge keys are not allowed", k.Line)})
			}
			checkNode(file, v, k.Value, errs)
		}
	case yaml.ScalarNode:
		if n.Tag == "!!float" {
			*errs = append(*errs, &Error{File: file, Field: field,
				Msg: fmt.Sprintf("line %d: %q is not a whole number (and no field takes a decimal; quote it if it is text)", n.Line, n.Value)})
		}
	}
}

var typeLineRE = regexp.MustCompile(`^line (\d+): (.*)$`)

// typeErrors turns yaml's "line 4: cannot unmarshal ..." into errors that name the field, read
// from the key on that line (yaml.v3 reports only the line).
func typeErrors(file string, raw []byte, err error) error {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return &Error{File: file, Msg: err.Error()}
	}
	lines := strings.Split(string(raw), "\n")
	var errs []error
	for _, m := range te.Errors {
		e := &Error{File: file, Msg: m}
		if sub := typeLineRE.FindStringSubmatch(m); sub != nil {
			n := 0
			_, _ = fmt.Sscanf(sub[1], "%d", &n)
			if n >= 1 && n <= len(lines) {
				if key, _, ok := strings.Cut(strings.TrimSpace(lines[n-1]), ":"); ok {
					e.Field, e.Msg = strings.Trim(key, `-"' `), fmt.Sprintf("line %s: %s", sub[1], sub[2])
				}
			}
		}
		errs = append(errs, e)
	}
	return errors.Join(errs...)
}

// ParseTicket decodes and validates the bytes of a ticket.yaml; file names it in errors. It does
// not know the directory the file came from (LoadTicket checks the id against it) nor the roles of
// catalog.yaml (Load checks those).
func ParseTicket(file string, raw []byte) (*Ticket, error) {
	var t Ticket
	if err := decodeStrict(file, raw, &t); err != nil {
		return nil, err
	}
	is := &issues{file: file}
	t.validate(is)
	if err := is.err(); err != nil {
		return nil, err
	}
	return &t, nil
}

func (t *Ticket) validate(is *issues) {
	switch {
	case len(t.ID) < minIDLen || len(t.ID) > maxIDLen:
		is.add("id", "must be %d to %d characters, got %d", minIDLen, maxIDLen, len(t.ID))
	case !idRE.MatchString(t.ID):
		is.add("id", "%q must be <role-slug>/<ticket-slug>, each part lower-case letters, digits and single hyphens", t.ID)
	}
	if err := checkLine(t.Title, MaxTitleChars); err != "" {
		is.add("title", "%s", err)
	}
	if t.Role == "" {
		is.add("role", "is required")
	} else if !slugRE.MatchString(t.Role) {
		is.add("role", "%q is not a role slug", t.Role)
	} else if idRE.MatchString(t.ID) && t.Role != strings.SplitN(t.ID, "/", 2)[0] {
		is.add("role", "is %q, but the id %q starts with %q", t.Role, t.ID, strings.SplitN(t.ID, "/", 2)[0])
	}
	if t.Order < 1 || t.Order > MaxOrder {
		is.add("order", "must be 1 to %d, got %d", MaxOrder, t.Order)
	}
	if t.EstMinutes < 1 || t.EstMinutes > MaxEstMinutes {
		is.add("est_minutes", "must be 1 to %d, got %d", MaxEstMinutes, t.EstMinutes)
	}
	if len(t.Tags) > MaxTags {
		is.add("tags", "at most %d tags, got %d", MaxTags, len(t.Tags))
	}
	seen := map[string]bool{}
	for i, tag := range t.Tags {
		switch {
		case !tagRE.MatchString(tag):
			is.add(fmt.Sprintf("tags[%d]", i), "%q must match [a-z0-9-]{1,24}", tag)
		case seen[tag]:
			is.add(fmt.Sprintf("tags[%d]", i), "%q appears twice", tag)
		}
		seen[tag] = true
	}
	switch n := len(t.Brief); {
	case strings.TrimSpace(t.Brief) == "":
		is.add("brief", "is required")
	case n > MaxBriefBytes:
		is.add("brief", "at most %d bytes, got %d", MaxBriefBytes, n)
	case !utf8.ValidString(t.Brief):
		is.add("brief", "is not valid UTF-8")
	}
	if len(t.Hints) < 1 || len(t.Hints) > MaxHints {
		is.add("hints", "needs 1 to %d hints, got %d", MaxHints, len(t.Hints))
	}
	for i, h := range t.Hints {
		if n := utf8.RuneCountInString(h); strings.TrimSpace(h) == "" || n > MaxHintChars {
			is.add(fmt.Sprintf("hints[%d]", i), "must be 1 to %d characters and not blank, got %d", MaxHintChars, n)
		}
	}
}

// checkLine returns what is wrong with a one-line text of 1..max characters, or "".
func checkLine(s string, maxChars int) string {
	n := utf8.RuneCountInString(s)
	switch {
	case strings.TrimSpace(s) == "":
		return "is required"
	case n > maxChars:
		return fmt.Sprintf("at most %d characters, got %d", maxChars, n)
	case strings.ContainsAny(s, "\r\n"):
		return "must be one line"
	case !utf8.ValidString(s):
		return "is not valid UTF-8"
	}
	return ""
}

// LoadTicket reads and validates the ticket at <role>/<ticket> with its setup.sh and check.sh. It
// is what the CLI runs for a local tree; it does not need catalog.yaml (Load does the cross checks).
func LoadTicket(catalog fs.FS, id string) (*Ticket, error) {
	if !idRE.MatchString(id) || len(id) > maxIDLen {
		return nil, fmt.Errorf("%w: %q (expected <role>/<ticket>)", ErrNotFound, id)
	}
	file := path.Join(id, ticketFileName)
	raw, err := fs.ReadFile(catalog, file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	t, err := ParseTicket(file, raw)
	if err != nil {
		return nil, err
	}
	if t.ID != id {
		return nil, &Error{File: file, Field: "id", Msg: fmt.Sprintf("is %q, want %q (the directory path)", t.ID, id)}
	}
	for _, f := range []struct {
		name string
		dst  *[]byte
	}{{"setup.sh", &t.Setup}, {"check.sh", &t.Check}} {
		b, err := fs.ReadFile(catalog, path.Join(id, f.name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		// A Windows checkout may turn LF into CRLF; bash would choke on the \r.
		*f.dst = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	}
	return t, nil
}
