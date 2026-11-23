package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

// panicDB fails the test if a transaction is opened: a refused entry must not reach the database.
type panicDB struct{ t *testing.T }

func (p panicDB) Begin(context.Context) (pgx.Tx, error) {
	p.t.Helper()
	p.t.Fatal("transaction opened for a refused entry")
	return nil, nil
}

func TestWithAuditRefusesBeforeWriting(t *testing.T) {
	op := Operator{ID: "00000000-0000-0000-0000-000000000001", Login: "ada"}
	ok := AuditEntry{Action: "flag.set", TargetType: "flag", TargetID: "ai_cap"}
	tests := []struct {
		name string
		op   Operator
		e    func(AuditEntry) AuditEntry
	}{
		{"no operator", Operator{}, func(e AuditEntry) AuditEntry { return e }},
		{"no action", op, func(e AuditEntry) AuditEntry { e.Action = ""; return e }},
		{"no target type", op, func(e AuditEntry) AuditEntry { e.TargetType = ""; return e }},
		{"no target id", op, func(e AuditEntry) AuditEntry { e.TargetID = ""; return e }},
		{"email value in before", op, func(e AuditEntry) AuditEntry { e.Before = map[string]any{"email": "a@b.c"}; return e }},
		{"username value in after", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"username": "ada"}; return e }},
		{"github login value", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"github_login": "ada"}; return e }},
		{"mixed case key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"NewEmail": "a@b.c"}; return e }},
		{"non-string value", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"username": 7}; return e }},
		{"nested", op, func(e AuditEntry) AuditEntry {
			e.After = map[string]any{"learner": map[string]any{"email": "a@b.c"}}
			return e
		}},
		{"channel", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"x": make(chan int)}; return e }},
		{"typed map", op, func(e AuditEntry) AuditEntry {
			e.After = map[string]any{"learner": map[string]string{"email": "a@b.c"}}
			return e
		}},
		{"struct", op, func(e AuditEntry) AuditEntry {
			e.After = map[string]any{"learner": struct{ Email string }{"a@b.c"}}
			return e
		}},
		{"slice", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"emails": []string{"a@b.c"}}; return e }},
		{"any slice", op, func(e AuditEntry) AuditEntry { e.Before = map[string]any{"x": []any{1}}; return e }},
		{"pointer", op, func(e AuditEntry) AuditEntry { s := "x"; e.After = map[string]any{"x": &s}; return e }},
		{"e-mail key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"e-mail": "x"}; return e }},
		{"user_name key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"user_name": "ada"}; return e }},
		{"User Name key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"User Name": "ada"}; return e }},
		{"mail key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"mail": "x"}; return e }},
		{"zero-width key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"em\u200bail": "ada"}; return e }},
		{"cyrillic lookalike key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"em\u0430il": "ada"}; return e }},
		{"full-width key", op, func(e AuditEntry) AuditEntry {
			e.After = map[string]any{"\uff45\uff4d\uff41\uff49\uff4c": "ada"}
			return e
		}},
		{"email as key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"ada@example.com": true}; return e }},
		{"empty key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"": 1}; return e }},
		{"handle key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"handle": "ada"}; return e }},
		{"name key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"name": "Ada"}; return e }},
		{"display.name key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"display.name": "Ada"}; return e }},
		{"address key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"address": "1 St"}; return e }},
		{"phone key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"phone": "600"}; return e }},
		{"github-login key", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"github-login": "ada"}; return e }},
		{"number under email", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"email": 1}; return e }},
		{"email-looking value", op, func(e AuditEntry) AuditEntry { e.After = map[string]any{"note": "ping ada@example.com"}; return e }},
		{"unchanged key", op, func(e AuditEntry) AuditEntry {
			e.Before = map[string]any{"plan": "free", "n": 1}
			e.After = map[string]any{"plan": "free", "n": 2}
			return e
		}},
		{"unchanged marker", op, func(e AuditEntry) AuditEntry {
			e.Before = map[string]any{"username": ChangedMarker}
			e.After = map[string]any{"username": ChangedMarker}
			return e
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			err := WithAudit(context.Background(), panicDB{t}, tt.op, tt.e(ok), func(pgx.Tx) error { called = true; return nil })
			if !errors.Is(err, ErrAuditEntry) || called {
				t.Fatalf("err = %v, action ran = %v", err, called)
			}
		})
	}
}

func TestAcceptedShapes(t *testing.T) {
	op := Operator{ID: "00000000-0000-0000-0000-000000000001", Login: "ada"}
	for _, e := range []AuditEntry{
		{Action: "a", TargetType: "t", TargetID: "1"},
		{Action: "a", TargetType: "t", TargetID: "1", After: map[string]any{"username": ChangedMarker}},
		{Action: "a", TargetType: "t", TargetID: "1", Before: map[string]any{"email": nil}, After: map[string]any{"email": ChangedMarker, "plan": "free"}},
		{Action: "a", TargetType: "t", TargetID: "1", Before: map[string]any{"plan": "free", "n": 1}, After: map[string]any{"plan": "pro", "n": 2, "on": true, "x": nil}},
		{Action: "a", TargetType: "t", TargetID: "1", After: map[string]any{"note": "a@b"}}, // no dot after the @
	} {
		if _, _, err := e.validate(op); err != nil {
			t.Errorf("%+v refused: %v", e, err)
		}
	}
}
