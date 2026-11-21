// Package repo is the database access layer: a pgx pool and the sqlc queries.
package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const publicOrg = "public"

// Store is the Postgres-backed data access.
type Store struct {
	pool *pgxpool.Pool
	q    *Queries
}

// Open connects to Postgres.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	return &Store{pool: pool, q: New(pool)}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ready reads the seeded org: it fails when the database is unreachable or not migrated.
func (s *Store) Ready(ctx context.Context) error {
	if _, err := s.q.GetOrgBySlug(ctx, publicOrg); err != nil {
		return fmt.Errorf("read org %q: %w", publicOrg, err)
	}
	return nil
}
