// Package dbpool opens every Postgres pool of the project with a bounded connect.
package dbpool

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ConnectTimeout bounds one connect (dial and handshake) unless the URL sets connect_timeout.
const ConnectTimeout = 5 * time.Second

// Config parses url and applies the default connect bound.
func Config(url string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = ConnectTimeout
	}
	return cfg, nil
}

// New opens a pool for url with a bounded connect. Use it instead of pgxpool.New.
func New(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := Config(url)
	if err != nil {
		return nil, err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}
