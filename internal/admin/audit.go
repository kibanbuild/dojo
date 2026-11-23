package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ChangedMarker is the only value an audit entry may hold under a key that names an email, username,
// login or similar: the log records that the field changed, never what it was or became.
const ChangedMarker = "changed"

// ErrAuditEntry means the entry was refused before anything was written.
var ErrAuditEntry = errors.New("invalid audit entry")

// AuditEntry describes one operator action for admin_audit. Before and After are flat maps of the
// changed fields only: every value is nil, a bool, a number or a string. A nested map, a slice, a
// struct or any other type is refused, so no personal value can hide inside a shape the guard does
// not read. The operator and its login come from the session operator, never from the caller.
type AuditEntry struct {
	Action     string
	TargetType string
	TargetID   string
	Reason     string // optional
	Before     map[string]any
	After      map[string]any
}

// Beginner starts a transaction; *pgxpool.Pool satisfies it.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WithAudit runs fn and writes the audit row in one transaction: both commit or neither does. The
// entry is validated before the transaction opens. fn runs first, so a failing audit insert rolls
// the action back, and a failing action leaves no audit row. The transaction is rolled back on every
// way out that is not a commit, a panic in fn included.
//
// fn must write only through the tx it is given, never through the pool: a write on another
// connection is outside the transaction and survives a rollback.
func WithAudit(ctx context.Context, db Beginner, op Operator, e AuditEntry, fn func(tx pgx.Tx) error) error {
	before, after, err := e.validate(op)
	if err != nil {
		return err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	// After a commit this is a no-op (pgx.ErrTxClosed); on an error return or a panic it rolls back.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(tx); err != nil {
		return err
	}
	var reason *string
	if e.Reason != "" {
		reason = &e.Reason
	}
	if _, err := tx.Exec(ctx, `INSERT INTO admin_audit
		(operator_id, operator_login, action, target_type, target_id, reason, before, after)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8)`,
		op.ID, op.Login, e.Action, e.TargetType, e.TargetID, reason, before, after); err != nil {
		return fmt.Errorf("audit insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (e AuditEntry) validate(op Operator) (before, after []byte, err error) {
	if op.ID == "" || op.Login == "" {
		return nil, nil, fmt.Errorf("%w: no operator", ErrAuditEntry)
	}
	if e.Action == "" || e.TargetType == "" || e.TargetID == "" {
		return nil, nil, fmt.Errorf("%w: action, target type and target id are required", ErrAuditEntry)
	}
	if err := checkFields("before", e.Before); err != nil {
		return nil, nil, err
	}
	if err := checkFields("after", e.After); err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(e.After))
	for k := range e.After {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if b, ok := e.Before[k]; ok && b == e.After[k] {
			return nil, nil, fmt.Errorf("%w: %q is in before and after with the same value; record changed fields only", ErrAuditEntry, k)
		}
	}
	if before, err = marshalOptional(e.Before); err != nil {
		return nil, nil, fmt.Errorf("%w: before: %w", ErrAuditEntry, err)
	}
	if after, err = marshalOptional(e.After); err != nil {
		return nil, nil, fmt.Errorf("%w: after: %w", ErrAuditEntry, err)
	}
	return before, after, nil
}

// checkFields enforces the flat-scalar shape, the sensitive-key rule and the email-looking-value rule.
func checkFields(name string, m map[string]any) error {
	for k, v := range m {
		if !plainKey(k) {
			return fmt.Errorf("%w: %s key %q must be 1 to 64 of a-z A-Z 0-9 _ . -", ErrAuditEntry, name, k)
		}
		switch x := v.(type) {
		case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
			if sensitiveKey(k) && x != nil {
				return fmt.Errorf("%w: %s.%s names personal data; record %q or nil", ErrAuditEntry, name, k, ChangedMarker)
			}
		case string:
			if sensitiveKey(k) {
				if x != ChangedMarker {
					return fmt.Errorf("%w: %s.%s names personal data; record %q", ErrAuditEntry, name, k, ChangedMarker)
				}
			} else if looksLikeEmail(x) {
				return fmt.Errorf("%w: %s.%s holds an email-looking value", ErrAuditEntry, name, k)
			}
		default:
			return fmt.Errorf("%w: %s.%s must be a scalar (nil, bool, number or string), got %T", ErrAuditEntry, name, k, v)
		}
	}
	return nil
}

// sensitiveWords match anywhere in the normalized key; "name" alone matches only as a whole key or
// a suffix (displayname, fullname). The match is deliberately broad: a key such as last_login_at or
// hostname is refused unless its value is the changed marker or nil, which is the safe side.
var sensitiveWords = []string{"email", "mail", "username", "login", "handle", "address", "phone"}

func sensitiveKey(k string) bool {
	n := strings.ToLower(k)
	for _, r := range []string{"_", "-", ".", " "} {
		n = strings.ReplaceAll(n, r, "")
	}
	for _, w := range sensitiveWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return n == "name" || strings.HasSuffix(n, "name")
}

// plainKey keeps keys to ASCII letters, digits, '_', '.' and '-', so a lookalike or zero-width
// character cannot split a sensitive word past sensitiveKey.
func plainKey(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for _, r := range k {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

// looksLikeEmail is true for an '@' followed somewhere later by a '.'.
func looksLikeEmail(s string) bool {
	i := strings.IndexByte(s, '@')
	return i >= 0 && strings.Contains(s[i+1:], ".")
}

// marshalOptional gives nil (SQL NULL) for a nil map.
func marshalOptional(m map[string]any) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}
