package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kibanbuild/dojo/internal/admin"
	"github.com/kibanbuild/dojo/internal/github"
)

// bootstrap is `dojo-admin bootstrap <github-login>`: a one-off task that writes the first operator
// and its audit row with the migration role (design 3.7). It never falls back to DATABASE_URL, which
// is the service's dojo_admin role, and it holds no OAuth credentials: the lookup is public.
func bootstrap(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: dojo-admin bootstrap <github-login>")
	}
	login := args[0]
	dsn := getenv("BOOTSTRAP_DATABASE_URL")
	if dsn == "" {
		return errors.New("BOOTSTRAP_DATABASE_URL is required: the migration role's connection string, not the service's")
	}
	if !admin.ValidLogin(login) {
		return admin.ErrInvalidLogin
	}
	cfg, err := github.LoadAPIConfig(getenv, getenv("DOJO_ENV") == "local")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open pool: %w", err)
	}
	defer pool.Close()

	id, err := admin.Bootstrap(ctx, github.New(cfg), admin.PGBootstrapStore{Pool: pool}, login)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "operator %s created\nbootstrap done: the first operator can now sign in\n", id)
	return nil
}
