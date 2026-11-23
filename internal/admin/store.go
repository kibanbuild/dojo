package admin

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound means no such active operator or session: unknown, revoked or expired all look alike.
var ErrNotFound = errors.New("not found")

// Operator is a row of operators that is not revoked.
type Operator struct {
	ID    string
	Login string
}

// Store is the data the operator sign-in needs. The database runs as dojo_admin.
type Store interface {
	// OperatorByGithubID returns the operator with that GitHub id, or ErrNotFound when there is none
	// or it is revoked.
	OperatorByGithubID(ctx context.Context, githubID int64) (Operator, error)
	// CreateSession stores the SHA-256 of a new session token for the operator. The database fixes
	// the times: one hour from now, no sliding.
	CreateSession(ctx context.Context, operatorID string, tokenHash []byte) error
	// ActiveSession returns the operator of a session that is neither revoked nor expired, or
	// ErrNotFound. It also refuses a session whose operator was revoked.
	ActiveSession(ctx context.Context, tokenHash []byte) (Operator, error)
	// RevokeSession ends a session; revoking an unknown or ended one is not an error.
	RevokeSession(ctx context.Context, tokenHash []byte) error
}

// PGStore is the Store over a pgx pool connected as dojo_admin.
type PGStore struct{ Pool *pgxpool.Pool }

// OperatorByGithubID implements Store.
func (s PGStore) OperatorByGithubID(ctx context.Context, githubID int64) (Operator, error) {
	return one(s.Pool.QueryRow(ctx,
		`SELECT id::text, github_login FROM operators WHERE github_id = $1 AND revoked_at IS NULL`, githubID))
}

// CreateSession implements Store.
func (s PGStore) CreateSession(ctx context.Context, operatorID string, tokenHash []byte) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO admin_sessions (operator_id, token_hash) VALUES ($1::uuid, $2)`, operatorID, tokenHash)
	return err
}

// ActiveSession implements Store. Expiry is judged on the database clock.
func (s PGStore) ActiveSession(ctx context.Context, tokenHash []byte) (Operator, error) {
	return one(s.Pool.QueryRow(ctx, `SELECT o.id::text, o.github_login
		FROM admin_sessions s JOIN operators o ON o.id = s.operator_id
		WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > now() AND o.revoked_at IS NULL`, tokenHash))
}

// RevokeSession implements Store.
func (s PGStore) RevokeSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.Pool.Exec(ctx, `UPDATE admin_sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash)
	return err
}

func one(row pgx.Row) (Operator, error) {
	var o Operator
	if err := row.Scan(&o.ID, &o.Login); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Operator{}, ErrNotFound
		}
		return Operator{}, err
	}
	return o, nil
}
