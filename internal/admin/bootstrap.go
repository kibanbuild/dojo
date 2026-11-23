package admin

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kibanbuild/dojo/internal/github"
)

var (
	// ErrInvalidLogin means the argument is not a GitHub login.
	ErrInvalidLogin = errors.New("not a valid GitHub login")
	// ErrOperatorsExist means an active operator exists: bootstrap is for the first one only.
	ErrOperatorsExist = errors.New("an active operator already exists; add operators from the admin service")
	// ErrWasRevoked means that account is a revoked operator; revocation is one-way.
	ErrWasRevoked = errors.New("that account is a revoked operator and revocation is one-way")
)

// loginRE is GitHub's rule: 1 to 39 letters, digits or single hyphens, none at either end.
var loginRE = regexp.MustCompile(`^[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)

// ValidLogin reports whether login has the shape of a GitHub login.
func ValidLogin(login string) bool { return len(login) <= 39 && loginRE.MatchString(login) }

// UserLookup resolves a login to its GitHub account.
type UserLookup interface {
	UserByLogin(ctx context.Context, login string) (github.User, error)
}

// BootstrapStore writes the first operator. It runs as the migration role, never as dojo_admin.
type BootstrapStore interface {
	// CreateFirstOperator inserts the operator and its audit row in one transaction and returns the
	// operator id, or ErrOperatorsExist and writes nothing.
	CreateFirstOperator(ctx context.Context, githubID int64, login string) (string, error)
}

// Bootstrap makes login the first operator and returns its id. The login is checked before any
// network call; the GitHub id and canonical login come from GitHub.
func Bootstrap(ctx context.Context, gh UserLookup, st BootstrapStore, login string) (string, error) {
	if !ValidLogin(login) {
		return "", ErrInvalidLogin
	}
	u, err := gh.UserByLogin(ctx, login)
	if err != nil {
		return "", err
	}
	return st.CreateFirstOperator(ctx, u.ID, u.Login)
}

// PGBootstrapStore is the BootstrapStore over a pool connected with the migration role.
type PGBootstrapStore struct{ Pool *pgxpool.Pool }

// CreateFirstOperator implements BootstrapStore.
func (s PGBootstrapStore) CreateFirstOperator(ctx context.Context, githubID int64, login string) (id string, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Two bootstraps at once must not both see an empty table.
	if _, err = tx.Exec(ctx, `LOCK TABLE operators IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return "", err
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM operators WHERE revoked_at IS NULL)`).Scan(&active); err != nil {
		return "", err
	}
	if active {
		return "", ErrOperatorsExist
	}
	err = tx.QueryRow(ctx, `INSERT INTO operators (github_id, github_login) VALUES ($1, $2) RETURNING id::text`,
		githubID, login).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrWasRevoked
	}
	if err != nil {
		return "", err
	}
	// Changed fields only, and a marker, never the login (design 3.3).
	if _, err = tx.Exec(ctx, `INSERT INTO admin_audit (operator_id, operator_login, action, target_type, target_id, after)
		VALUES ($1::uuid, $2, 'operator.bootstrap', 'operator', $1, '{"github_id":"changed","github_login":"changed"}'::jsonb)`,
		id, login); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return id, nil
}
